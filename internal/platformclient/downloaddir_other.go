//go:build !windows

package platformclient

// defaultDownloadDir 在非 Windows 平台使用应用缓存目录保存下载文件。
func defaultDownloadDir() (string, error) {
	return cacheDownloadDir()
}
