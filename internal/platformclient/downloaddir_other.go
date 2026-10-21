//go:build !windows

package platformclient

func defaultDownloadDir() (string, error) {
	return cacheDownloadDir()
}
