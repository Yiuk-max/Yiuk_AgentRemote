package platformclient

import (
	"testing"

	"agentremote/internal/agenttools"
)

// agenttools 导出的「必须由后端实现」清单，与本包的分发表必须完全一致。
// 新增工具时如果只在目录里登记、忘了实现，这条测试会直接失败。
func TestEveryBackendToolHasAHandler(t *testing.T) {
	client, err := New(Config{Address: "127.0.0.1:1", OperatorPassword: "0123456789abcdef"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, name := range agenttools.BackendTools() {
		if _, ok := client.toolHandlers[name]; !ok {
			t.Fatalf("tool %q is declared as backend-implemented but has no handler", name)
		}
	}
	for name := range client.toolHandlers {
		found := false
		for _, backendName := range agenttools.BackendTools() {
			if backendName == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("handler %q is not in agenttools.BackendTools()", name)
		}
	}
}
