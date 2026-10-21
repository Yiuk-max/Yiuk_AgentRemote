package platformclient

import (
	"os"
	"path/filepath"
)

// cacheDownloadDir is the fallback used when the platform has no known
// Downloads folder (for example on non-Windows systems).
func cacheDownloadDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Yiuk", "DeviceAssist", "downloads"), nil
}
