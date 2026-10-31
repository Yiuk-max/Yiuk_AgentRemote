//go:build windows

package platformclient

import "golang.org/x/sys/windows"

// defaultDownloadDir 使用用户配置的 Windows Downloads 已知文件夹
// 即使用户将其移动到其他驱动器，也会将下载文件保存到预期位置，而不是应用缓存目录
func defaultDownloadDir() (string, error) {
	path, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
	if err == nil && path != "" {
		return path, nil
	}
	return cacheDownloadDir()
}
