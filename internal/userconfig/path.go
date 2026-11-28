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

// ProgramDataRoot 是程序本地数据目录：**可执行文件旁边的 data\**。
// 绿色目录里所有配置、缓存、下载都跟着程序走；目录不可写时才回退用户目录。
func ProgramDataRoot() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	root := filepath.Join(filepath.Dir(executable), "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ""
	}
	probe := filepath.Join(root, ".yiuk-write-test")
	if err := os.WriteFile(probe, []byte("1"), 0o600); err != nil {
		return ""
	}
	_ = os.Remove(probe)
	return root
}

// Candidates returns the possible paths for name in preference order.
//
// 顺序：程序目录 data\（绿色版）→ %LOCALAPPDATA% → %APPDATA% → os.UserConfigDir。
// 新写入落在第一个可写位置；后面的只用于读取旧配置（升级后仍能找到设备身份）。
func Candidates(name string) []string {
	if name == "" {
		return nil
	}
	out := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	if root := ProgramDataRoot(); root != "" {
		add(filepath.Join(root, name))
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
	for _, root := range roots {
		add(filepath.Join(root, "Yiuk", "DeviceAssist", name))
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
