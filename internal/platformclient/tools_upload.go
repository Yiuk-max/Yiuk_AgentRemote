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

// handleUploadFile 解析工具参数，将本地文件发送到目标设备。
// 平台按顺序转发 base64 分块；设备将其写入 remote_path（为空时写入设备自己的 uploads 目录），并返回最终路径、大小和 SHA-256。
func (c *Client) handleUploadFile(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		SessionID  string `json:"session_id"`
		LocalPath  string `json:"local_path"`
		RemotePath string `json:"remote_path"`
		Overwrite  bool   `json:"overwrite"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	localPath := strings.TrimSpace(input.LocalPath)
	if localPath == "" || !filepath.IsAbs(localPath) {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "local_path must be an absolute path"}
	}
	if len(input.RemotePath) > 4096 {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "remote_path exceeds 4096 characters"}
	}
	file, fileInfo, fileSHA256, err := prepareUploadFile(localPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	requestID := randomID()
	chunksSent := 0
	response, err := c.sendStreamingRequest(ctx, protocol.Message{
		Type:       protocol.TypeUploadFile,
		SessionID:  input.SessionID,
		RequestID:  requestID,
		RemotePath: input.RemotePath,
		FileName:   fileInfo.Name(),
		FileSize:   fileInfo.Size(),
		FileSHA256: fileSHA256,
		Overwrite:  input.Overwrite,
	}, func(sendFrame func(protocol.Message) error) error {
		if fileInfo.Size() == 0 {
			// 设备根据请求元数据创建空文件；空的协议分块不是有效的传输帧
			return nil
		}
		// 提前读取一个分块，以便文件大小恰好是分块大小整数倍时，仍能将 EOF 标记在最后一块上
		currentChunk, err := readUploadChunk(file)
		if err != nil {
			return err
		}
		var sequence uint64 = 1
		for {
			nextChunk, nextChunkErr := readUploadChunk(file)
			isLastChunk := nextChunkErr != nil
			if nextChunkErr != nil && !errors.Is(nextChunkErr, io.EOF) {
				return nextChunkErr
			}
			if err := sendFrame(protocol.Message{
				Type:      protocol.TypeFileUploadChunk,
				SessionID: input.SessionID,
				RequestID: requestID,
				Sequence:  sequence,
				Data:      base64.StdEncoding.EncodeToString(currentChunk),
				EOF:       isLastChunk,
			}); err != nil {
				return err
			}
			sequence++
			chunksSent++
			if isLastChunk {
				return nil
			}
			currentChunk = nextChunk
		}
	}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeFileUploadResult && message.RequestID == requestID
	}, c.fileTransferTimeout())
	if err != nil {
		return nil, err
	}

	if response.Status != string(protocol.TaskSucceeded) || !response.ResultKnown {
		if response.Code != "" {
			return nil, &agenttools.Error{Code: response.Code, Message: response.Message}
		}
		return nil, &agenttools.Error{Code: "FILE_UPLOAD_FAILED", Message: "file upload did not complete successfully (status " + response.Status + ")"}
	}
	if response.FileSize != fileInfo.Size() {
		return nil, &agenttools.Error{Code: "FILE_INTEGRITY_FAILED", Message: fmt.Sprintf("device wrote %d bytes, expected %d", response.FileSize, fileInfo.Size())}
	}
	remoteSHA256 := strings.ToLower(strings.TrimSpace(response.FileSHA256))
	if remoteSHA256 != "" && remoteSHA256 != fileSHA256 {
		return nil, &agenttools.Error{Code: "FILE_INTEGRITY_FAILED", Message: "device checksum does not match the uploaded file"}
	}
	remotePath := response.RemotePath
	if remotePath == "" {
		remotePath = input.RemotePath
	}
	return map[string]any{
		"session_id":  input.SessionID,
		"request_id":  requestID,
		"local_path":  localPath,
		"remote_path": remotePath,
		"file_name":   response.FileName,
		"size":        response.FileSize,
		"sha256":      fileSHA256,
		"chunks":      chunksSent,
		"overwrite":   input.Overwrite,
		"status":      response.Status,
	}, nil
}

// readUploadChunk 最多读取一个上传分块
// 仅当没有剩余数据时才返回 io.EOF，以便调用方将前一个分块标记为最后一块
func readUploadChunk(file *os.File) ([]byte, error) {
	buffer := make([]byte, uploadChunkBytes)
	bytesRead, err := io.ReadFull(file, buffer)
	if bytesRead > 0 {
		return buffer[:bytesRead], nil
	}
	if err == nil {
		return nil, io.EOF
	}
	return nil, err
}

// prepareUploadFile 打开并验证本地文件，计算 SHA-256 后将文件指针复位，供后续流式上传。
func prepareUploadFile(localPath string) (*os.File, os.FileInfo, string, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "could not open local_path"}
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "could not inspect local_path"}
	}
	if !fileInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "local_path must be a regular file"}
	}
	if fileInfo.Size() > maxUploadFileBytes {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "FILE_TOO_LARGE", Message: "local file exceeds the 32 MiB upload limit"}
	}
	checksum := sha256.New()
	if _, err := io.Copy(checksum, file); err != nil {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "could not read local_path"}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, nil, "", &agenttools.Error{Code: "LOCAL_READ_FAILED", Message: "could not rewind local_path"}
	}
	return file, fileInfo, hex.EncodeToString(checksum.Sum(nil)), nil
}
