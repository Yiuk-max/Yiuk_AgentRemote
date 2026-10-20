//go:build !linux && !windows

package agent

import (
	"runtime"
	"time"
)

func systemMetrics() map[string]any {
	return map[string]any{"available": false, "message": "system metrics are currently supported on Linux agents", "cpu_count": runtime.NumCPU(), "sampled_at": time.Now().UTC()}
}
