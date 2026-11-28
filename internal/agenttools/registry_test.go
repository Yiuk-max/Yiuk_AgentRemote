package agenttools

import "testing"

// 目录、本地工具、组合工具、后端工具四者必须严丝合缝。
// 加工具时最容易忘的就是某个分发层，这条测试就是拦这个的。
func TestEveryToolHasExactlyOneHandler(t *testing.T) {
	for _, spec := range toolSpecs {
		handled := 0
		if _, ok := localTools[spec.Name]; ok {
			handled++
		}
		if _, ok := composedTools[spec.Name]; ok {
			handled++
		}
		for _, name := range backendTools() {
			if name == spec.Name {
				handled++
			}
		}
		if handled != 1 {
			t.Fatalf("%s is handled by %d layers, want exactly 1", spec.Name, handled)
		}
	}

	// 反过来：注册表里不能有目录之外的工具名（改名后忘了删会走到这里）。
	for name := range localTools {
		if _, ok := specByName[name]; !ok {
			t.Fatalf("local tool %q is not in the catalog", name)
		}
	}
	for name := range composedTools {
		if _, ok := specByName[name]; !ok {
			t.Fatalf("composed tool %q is not in the catalog", name)
		}
	}
}

// 组合工具依赖后端（除了 wait），所以必须有 backend 才能用；
// 这条测试同时固定「wait 在 backend 判空之前处理」这个顺序。
func TestLocalToolsNeedNoBackend(t *testing.T) {
	core := NewCore(nil)
	response := core.Call(t.Context(), Wait, mustJSON(map[string]any{"ms": 0}))
	if !response.OK {
		t.Fatalf("wait without a backend: %#v", response)
	}
	cases := []struct {
		name  string
		input map[string]any
	}{
		{TakeScreenshot, map[string]any{"session_id": "s"}},
		{ListDir, map[string]any{"session_id": "s", "path": "/"}},
	}
	for _, tc := range cases {
		response := core.Call(t.Context(), tc.name, mustJSON(tc.input))
		if response.OK || response.Code != "NOT_CONFIGURED" {
			t.Fatalf("%s without a backend: got %#v, want NOT_CONFIGURED", tc.name, response)
		}
	}
}
