package userconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 绿色版优先：程序目录 data\ 里的文件先被找到；旧位置（LOCALAPPDATA / APPDATA）
// 仍然可读，保证升级后还能拿到原来的设备身份。
func TestPathPrefersProgramDataThenLegacyLocations(t *testing.T) {
	local := t.TempDir()
	roaming := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)

	localPath := filepath.Join(local, "Yiuk", "DeviceAssist", "platform-client.json")
	roamingPath := filepath.Join(roaming, "Yiuk", "DeviceAssist", "platform-client.json")

	portableRoot := ProgramDataRoot()
	portablePath := ""
	if portableRoot != "" {
		portablePath = filepath.Join(portableRoot, "platform-client.json")
	}

	// 什么都不存在时，第一个候选就是程序目录（新写入的落点）。
	first := Candidates("platform-client.json")
	if portablePath != "" {
		if len(first) == 0 || first[0] != portablePath {
			t.Fatalf("first candidate = %v, want the program data path %q", first, portablePath)
		}
	} else if runtime.GOOS == "windows" && (len(first) == 0 || first[0] != localPath) {
		t.Fatalf("without a writable program dir the first candidate should be %q, got %v", localPath, first)
	}

	writeFile(t, roamingPath)
	if got, err := Path("platform-client.json"); err != nil || got != roamingPath {
		t.Fatalf("legacy roaming path should be readable: %q %v", got, err)
	}

	writeFile(t, localPath)
	if got, err := Path("platform-client.json"); err != nil || got != localPath {
		t.Fatalf("legacy local path should win over roaming: %q %v", got, err)
	}

	if portablePath != "" {
		writeFile(t, portablePath)
		if got, err := Path("platform-client.json"); err != nil || got != portablePath {
			t.Fatalf("program data path should win over legacy locations: %q %v", got, err)
		}
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}
