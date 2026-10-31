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

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// maxUploadFileBytes 与服务端 max_upload_bytes 的默认值一致，避免传输平台会拒绝的文件
// 服务端仍会执行自身配置的大小限制
const maxUploadFileBytes = 32 << 20

// uploadChunkBytes 确保每个 base64 分块远小于协议帧上限；解码后的 64 KiB 约对应 87 KiB 编码数据
const uploadChunkBytes = 64 << 10

// uploadFile 将本地文件发送到目标设备
// 平台按顺序转发 base64 分块；设备将其写入 remote_path（为空时写入设备自己的 uploads 目录），并返回最终路径、大小和 SHA-256
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
			// 设备根据请求元数据创建空文件；空的协议分块不是有效的传输帧
			return nil
		}
		// 提前读取一个分块，以便文件大小恰好是分块大小整数倍时，仍能将 EOF 标记在最后一块上
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

// readUploadChunk 最多读取一个上传分块
// 仅当没有剩余数据时才返回 io.EOF，以便调用方将前一个分块标记为最后一块
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

// openUploadSource 验证本地文件、计算哈希并将文件指针复位
// 确保流式传输的数据与声明的摘要及大小一致
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
