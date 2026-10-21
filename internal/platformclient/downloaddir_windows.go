//go:build windows

package platformclient

import "golang.org/x/sys/windows"

// defaultDownloadDir follows the user's configured Windows Downloads known
// folder, including when the user has moved it to another drive. This keeps
// downloaded files where the user expects them instead of in the app cache.
func defaultDownloadDir() (string, error) {
	path, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
	if err == nil && path != "" {
		return path, nil
	}
	return cacheDownloadDir()
}
