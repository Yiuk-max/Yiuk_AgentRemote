package platformclient

import (
	"os"
	"path/filepath"
)

// 当平台未能确定 Downloads 目录时使用 cacheDownloadDir 作为回退路径（例如非 Windows 系统）
func cacheDownloadDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Yiuk", "DeviceAssist", "downloads"), nil
}
