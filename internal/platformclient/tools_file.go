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

// downloadFile 从设备请求一个受大小限制的文件
// 平台以事件形式流式返回 base64 分块；收到对应的最终响应后，再将数据原子地写入本地文件
func (c *Client) downloadFile(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		SessionID       string `json:"session_id"`
		RemotePath      string `json:"remote_path"`
		DestinationPath string `json:"destination_path"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
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
		root, err := defaultDownloadDir()
		if err != nil {
			return nil, &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not determine a local download directory"}
		}
		destination = filepath.Join(root, fmt.Sprintf("%d-%s", time.Now().UnixNano(), safeRemoteBase(input.RemotePath)))
		if _, err := os.Lstat(destination); err == nil {
			return nil, &agenttools.Error{Code: "LOCAL_PATH_EXISTS", Message: "generated download path already exists; retry"}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not inspect the download directory"}
		}
	}

	requestID := randomID()
	resp, err := c.callTimeout(ctx, protocol.Message{
		Type: protocol.TypeDownloadFile, SessionID: input.SessionID, RequestID: requestID, RemotePath: input.RemotePath,
	}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeFileDownloadResult && m.RequestID == requestID
	}, c.fileTransferTimeout())
	if err != nil {
		return nil, err
	}

	c.cacheMu.Lock()
	d := c.download(input.SessionID, requestID)
	status := d.status
	fileName := d.fileName
	resultKnown := d.resultKnown
	remoteSHA := strings.ToLower(strings.TrimSpace(d.fileSHA256))
	chunks := append([][]byte(nil), d.chunks...)
	c.cacheMu.Unlock()

	if status != string(protocol.TaskSucceeded) || !resultKnown {
		if resp.Code != "" {
			return nil, &agenttools.Error{Code: resp.Code, Message: resp.Message}
		}
		return nil, &agenttools.Error{Code: "FILE_DOWNLOAD_FAILED", Message: "file download did not complete successfully (status " + status + ")"}
	}
	if fileName == "" {
		fileName = safeRemoteBase(input.RemotePath)
	}

	hash := sha256.New()
	var total int64
	for _, chunk := range chunks {
		total += int64(len(chunk))
	}
	if resp.FileSize > 0 && resp.FileSize != total {
		return nil, &agenttools.Error{Code: "FILE_INTEGRITY_FAILED", Message: "received file size does not match the device result"}
	}
	if err := writeDownload(destination, chunks, hash); err != nil {
		return nil, err
	}
	localSHA := hex.EncodeToString(hash.Sum(nil))
	if remoteSHA != "" && remoteSHA != localSHA {
		_ = os.Remove(destination)
		return nil, &agenttools.Error{Code: "FILE_INTEGRITY_FAILED", Message: "file checksum does not match the device result"}
	}
	return map[string]any{
		"session_id":  input.SessionID,
		"request_id":  requestID,
		"remote_path": input.RemotePath,
		"file_name":   fileName,
		"local_path":  destination,
		"size":        total,
		"sha256":      localSHA,
		"status":      status,
	}, nil
}

func writeDownload(destination string, chunks [][]byte, hash io.Writer) error {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not create the download directory"}
	}
	tmp, err := os.CreateTemp(dir, ".yiuk-download-*")
	if err != nil {
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not create a temporary download file"}
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	for _, chunk := range chunks {
		if _, err := tmp.Write(chunk); err != nil {
			cleanup()
			return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not write the downloaded file"}
		}
		if _, err := hash.Write(chunk); err != nil {
			cleanup()
			return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not hash the downloaded file"}
		}
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not flush the downloaded file"}
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not close the downloaded file"}
	}
	if err := os.Rename(tmpName, destination); err != nil {
		_ = os.Remove(tmpName)
		return &agenttools.Error{Code: "LOCAL_WRITE_FAILED", Message: "could not move the downloaded file into place"}
	}
	return nil
}

func safeRemoteBase(remotePath string) string {
	base := path.Base(strings.ReplaceAll(remotePath, "\\", "/"))
	base = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, base)
	base = strings.TrimSpace(strings.Trim(base, "."))
	if base == "" {
		return "download.bin"
	}
	if len(base) > 160 {
		base = base[:160]
	}
	return base
}

// fileTransferTimeout 限制一次完整文件传输（下载或上传）的最长时间
func (c *Client) fileTransferTimeout() time.Duration {
	timeout := time.Duration(c.cfg.TimeoutSeconds) * time.Second
	if timeout < 5*time.Minute {
		timeout = 5 * time.Minute
	}
	return timeout
}
