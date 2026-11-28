//go:build windows

package platformclient

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"agentremote/internal/userconfig"
)

// defaultDownloadDir 使用用户配置的 Windows Downloads 已知文件夹
// 即使用户将其移动到其他驱动器，也会将下载文件保存到预期位置，而不是应用缓存目录
func defaultDownloadDir() (string, error) {
	// 程序本地优先：<exe>\data\downloads，绿色目录删掉就什么都不剩。
	if root := userconfig.ProgramDataRoot(); root != "" {
		path := filepath.Join(root, "downloads")
		if err := os.MkdirAll(path, 0o755); err == nil {
			return path, nil
		}
	}
	path, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
	if err == nil && path != "" {
		return path, nil
	}
	return cacheDownloadDir()
}
