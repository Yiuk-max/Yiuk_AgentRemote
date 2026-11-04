package platformclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
)

// handleDownloadFile 解析工具参数，从设备下载文件并保存到本地。
// 平台以事件形式流式返回 base64 分块；收到对应的最终响应后，再将数据原子地写入本地文件。
func (c *Client) handleDownloadFile(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		SessionID       string `json:"session_id"`
		RemotePath      string `json:"remote_path"`
		DestinationPath string `json:"destination_path"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	if strings.TrimSpace(input.RemotePath) == "" || len(input.RemotePath) > 4096 {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "remote_path is required and bounded"}
	}
	destination := strings.TrimSpace(input.DestinationPath)
	if destination != "" {
		if !filepath.IsAbs(destination) {
			return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "destination_path must be absolute"}
		}
		if _, err := os.Lstat(destination); err == nil {
			return nil, &agenttools.Error{Code: "LOCAL_PATH_EXISTS", Message: "destination_path already exists; refusing to overwrite"}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not inspect destination_path"}
		}
	} else {
		downloadDirectory, err := defaultDownloadDir()
		if err != nil {
			return nil, &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not determine a local download directory"}
		}
		destination = filepath.Join(downloadDirectory, fmt.Sprintf("%d-%s", time.Now().UnixNano(), sanitizeRemoteFileName(input.RemotePath)))
		if _, err := os.Lstat(destination); err == nil {
			return nil, &agenttools.Error{Code: "LOCAL_PATH_EXISTS", Message: "generated download path already exists; retry"}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not inspect the download directory"}
		}
	}

	requestID := randomID()
	response, err := c.sendRequestWithTimeout(ctx, protocol.Message{
		Type: protocol.TypeDownloadFile, SessionID: input.SessionID, RequestID: requestID, RemotePath: input.RemotePath,
	}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeFileDownloadResult && message.RequestID == requestID
	}, c.fileTransferTimeout())
	if err != nil {
		return nil, err
	}

	c.cacheMu.Lock()
	download := c.getOrCreateDownloadCache(input.SessionID, requestID)
	status := download.status
	fileName := download.fileName
	resultKnown := download.resultKnown
	remoteSHA256 := strings.ToLower(strings.TrimSpace(download.fileSHA256))
	chunks := append([][]byte(nil), download.chunks...)
	c.cacheMu.Unlock()

	if status != string(protocol.TaskSucceeded) || !resultKnown {
		if response.Code != "" {
			return nil, &agenttools.Error{Code: response.Code, Message: response.Message}
		}
		return nil, &agenttools.Error{Code: "FILE_DOWNLOAD_FAILED", Message: "file download did not complete successfully (status " + status + ")"}
	}
	if fileName == "" {
		fileName = sanitizeRemoteFileName(input.RemotePath)
	}

	checksum := sha256.New()
	var receivedBytes int64
	for _, chunk := range chunks {
		receivedBytes += int64(len(chunk))
	}
	if response.FileSize > 0 && response.FileSize != receivedBytes {
		return nil, &agenttools.Error{Code: "FILE_INTEGRITY_FAILED", Message: "received file size does not match the device result"}
	}
	if err := saveDownloadAtomically(destination, chunks, checksum); err != nil {
		return nil, err
	}
	localSHA256 := hex.EncodeToString(checksum.Sum(nil))
	if remoteSHA256 != "" && remoteSHA256 != localSHA256 {
		_ = os.Remove(destination)
		return nil, &agenttools.Error{Code: "FILE_INTEGRITY_FAILED", Message: "file checksum does not match the device result"}
	}
	return map[string]any{
		"session_id":  input.SessionID,
		"request_id":  requestID,
		"remote_path": input.RemotePath,
		"file_name":   fileName,
		"local_path":  destination,
		"size":        receivedBytes,
		"sha256":      localSHA256,
		"status":      status,
	}, nil
}

// saveDownloadAtomically 将分块写入同目录临时文件，完成同步后再原子替换为目标文件。
func saveDownloadAtomically(destination string, chunks [][]byte, checksum io.Writer) error {
	destinationDirectory := filepath.Dir(destination)
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not create the download directory"}
	}
	temporaryFile, err := os.CreateTemp(destinationDirectory, ".yiuk-download-*")
	if err != nil {
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not create a temporary download file"}
	}
	temporaryPath := temporaryFile.Name()
	cleanup := func() {
		_ = temporaryFile.Close()
		_ = os.Remove(temporaryPath)
	}
	for _, chunk := range chunks {
		if _, err := temporaryFile.Write(chunk); err != nil {
			cleanup()
			return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not write the downloaded file"}
		}
		if _, err := checksum.Write(chunk); err != nil {
			cleanup()
			return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not hash the downloaded file"}
		}
	}
	if err := temporaryFile.Sync(); err != nil {
		cleanup()
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not flush the downloaded file"}
	}
	if err := temporaryFile.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not close the downloaded file"}
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		_ = os.Remove(temporaryPath)
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not move the downloaded file into place"}
	}
	return nil
}

// sanitizeRemoteFileName 从远端路径提取适合作为本地文件名的部分，并替换非法字符。
func sanitizeRemoteFileName(remotePath string) string {
	fileName := path.Base(strings.ReplaceAll(remotePath, "\\", "/"))
	fileName = strings.Map(func(character rune) rune {
		if character < 32 || strings.ContainsRune(`<>:"/\|?*`, character) {
			return '_'
		}
		return character
	}, fileName)
	fileName = strings.TrimSpace(strings.Trim(fileName, "."))
	if fileName == "" {
		return "download.bin"
	}
	if len(fileName) > 160 {
		fileName = fileName[:160]
	}
	return fileName
}

// fileTransferTimeout 限制一次完整文件传输（下载或上传）的最长时间
func (c *Client) fileTransferTimeout() time.Duration {
	timeout := time.Duration(c.config.TimeoutSeconds) * time.Second
	if timeout < 5*time.Minute {
		timeout = 5 * time.Minute
	}
	return timeout
}
