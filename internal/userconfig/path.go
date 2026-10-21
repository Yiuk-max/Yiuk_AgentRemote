// Package userconfig resolves the per-user Yiuk configuration directory.
//
// The Qt client writes to %LOCALAPPDATA%\Yiuk\DeviceAssist and keeps the old
// %APPDATA%\Yiuk\DeviceAssist location readable. Go's os.UserConfigDir returns
// %APPDATA% on Windows, so CLI/MCP processes must check LOCALAPPDATA first to
// see the file the GUI just wrote.
package userconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// Candidates returns the possible paths for name in preference order.
func Candidates(name string) []string {
	if name == "" {
		return nil
	}
	var roots []string
	if runtime.GOOS == "windows" {
		if root := os.Getenv("LOCALAPPDATA"); root != "" {
			roots = append(roots, root)
		}
		if root := os.Getenv("APPDATA"); root != "" {
			roots = append(roots, root)
		}
	}
	if root, err := os.UserConfigDir(); err == nil {
		roots = append(roots, root)
	}
	out := make([]string, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		path := filepath.Join(root, "Yiuk", "DeviceAssist", name)
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

// Path returns the first existing candidate, or the preferred path when none
// exists so callers can report a useful path in an error.
func Path(name string) (string, error) {
	candidates := Candidates(name)
	if len(candidates) == 0 {
		return "", errors.New("could not determine the per-user configuration directory")
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return candidates[0], nil
}
