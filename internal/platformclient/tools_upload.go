package platformclient

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/platform/protocol"
)

// maxUploadFileBytes mirrors the server-side default for max_upload_bytes so a
// file the platform would reject is never streamed. The server still enforces
// its own configured limit.
const maxUploadFileBytes = 32 << 20

// uploadChunkBytes keeps every base64 chunk well below the protocol frame
// limit; 64 KiB decoded becomes ~87 KiB encoded.
const uploadChunkBytes = 64 << 10

// uploadFile pushes one local file to the target device. The platform forwards
// base64 chunks in order; the device writes them to remote_path (or to its own
// uploads directory when remote_path is empty) and reports the final path,
// size and SHA-256.
func (c *Client) uploadFile(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID  string `json:"session_id"`
		LocalPath  string `json:"local_path"`
		RemotePath string `json:"remote_path"`
		Overwrite  bool   `json:"overwrite"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	localPath := strings.TrimSpace(input.LocalPath)
	if localPath == "" || !filepath.IsAbs(localPath) {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "local_path must be an absolute path"}
	}
	if len(input.RemotePath) > 4096 {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "remote_path exceeds 4096 characters"}
	}
	file, info, digest, err := openUploadSource(localPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	requestID := randomID()
	chunksSent := 0
	resp, err := c.callStream(ctx, protocol.Message{
		Type:       protocol.TypeUploadFile,
		SessionID:  input.SessionID,
		RequestID:  requestID,
		RemotePath: input.RemotePath,
		FileName:   info.Name(),
		FileSize:   info.Size(),
		FileSHA256: digest,
		Overwrite:  input.Overwrite,
	}, func(write func(protocol.Message) error) error {
		if info.Size() == 0 {
			// The device creates the empty file from the request metadata; an
			// empty protocol chunk is not a valid transfer frame.
			return nil
		}
		// Read one chunk ahead so the final chunk can carry EOF even when the
		// file size is an exact multiple of the chunk size.
		pending, err := readUploadChunk(file)
		if err != nil {
			return err
		}
		var sequence uint64 = 1
		for {
			next, nextErr := readUploadChunk(file)
			eof := nextErr != nil
			if nextErr != nil && !errors.Is(nextErr, io.EOF) {
				return nextErr
			}
			if err := write(protocol.Message{
				Type:      protocol.TypeFileUploadChunk,
				SessionID: input.SessionID,
				RequestID: requestID,
				Sequence:  sequence,
				Data:      base64.StdEncoding.EncodeToString(pending),
				EOF:       eof,
			}); err != nil {
				return err
			}
			sequence++
			chunksSent++
			if eof {
				return nil
			}
			pending = next
		}
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeFileUploadResult && m.RequestID == requestID
	}, c.fileTransferTimeout())
	if err != nil {
		return nil, err
	}

	if resp.Status != string(protocol.TaskSucceeded) || !resp.ResultKnown {
		if resp.Code != "" {
			return nil, &agenttools.Error{Code: resp.Code, Message: resp.Message}
		}
		return nil, &agenttools.Error{Code: "FILE_UPLOAD_FAILED", Message: "file upload did not complete successfully (status " + resp.Status + ")"}
	}
	if resp.FileSize != info.Size() {
		return nil, &agenttools.Error{Code: "FILE_INTEGRITY_FAILED", Message: fmt.Sprintf("device wrote %d bytes, expected %d", resp.FileSize, info.Size())}
	}
	remoteSHA := strings.ToLower(strings.TrimSpace(resp.FileSHA256))
	if remoteSHA != "" && remoteSHA != digest {
		return nil, &agenttools.Error{Code: "FILE_INTEGRITY_FAILED", Message: "device checksum does not match the uploaded file"}
	}
	remotePath := resp.RemotePath
	if remotePath == "" {
		remotePath = input.RemotePath
	}
	return map[string]any{
		"session_id":  input.SessionID,
		"request_id":  requestID,
		"local_path":  localPath,
		"remote_path": remotePath,
		"file_name":   resp.FileName,
		"size":        resp.FileSize,
		"sha256":      digest,
		"chunks":      chunksSent,
		"overwrite":   input.Overwrite,
		"status":      resp.Status,
	}, nil
}

// readUploadChunk reads at most one upload chunk. It returns io.EOF only when
// no bytes are left, so the caller can flag the previous chunk as the last one.
func readUploadChunk(file *os.File) ([]byte, error) {
	buffer := make([]byte, uploadChunkBytes)
	n, err := io.ReadFull(file, buffer)
	if n > 0 {
		return buffer[:n], nil
	}
	if err == nil {
		return nil, io.EOF
	}
	return nil, err
}

// openUploadSource validates the local file, hashes it and rewinds it so the
// streamed bytes match the advertised digest and size.
func openUploadSource(localPath string) (*os.File, os.FileInfo, string, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "could not open local_path"}
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "could not inspect local_path"}
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "local_path must be a regular file"}
	}
	if info.Size() > maxUploadFileBytes {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "FILE_TOO_LARGE", Message: "local file exceeds the 32 MiB upload limit"}
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "could not read local_path"}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "could not rewind local_path"}
	}
	return file, info, hex.EncodeToString(hash.Sum(nil)), nil
}
