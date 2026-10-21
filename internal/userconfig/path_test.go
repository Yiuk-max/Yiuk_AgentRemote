package userconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPathPrefersLocalAppDataAndFallsBackToRoaming(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("LOCALAPPDATA/APPDATA compatibility is Windows-specific")
	}
	local := t.TempDir()
	roaming := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)

	localPath := filepath.Join(local, "Yiuk", "DeviceAssist", "platform-client.json")
	roamingPath := filepath.Join(roaming, "Yiuk", "DeviceAssist", "platform-client.json")
	if got, err := Path("platform-client.json"); err != nil || got != localPath {
		t.Fatalf("missing files should return the preferred local path: %q %v", got, err)
	}

	if err := os.MkdirAll(filepath.Dir(roamingPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(roamingPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Path("platform-client.json"); err != nil || got != roamingPath {
		t.Fatalf("legacy roaming path should be readable: %q %v", got, err)
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Path("platform-client.json"); err != nil || got != localPath {
		t.Fatalf("local path should win when both exist: %q %v", got, err)
	}
}
