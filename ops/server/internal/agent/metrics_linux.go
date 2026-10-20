//go:build linux

package agent

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var cpuSample struct {
	sync.Mutex
	idle, total uint64
	at          time.Time
}

func systemMetrics() map[string]any {
	m := map[string]any{"cpu_percent": 0.0, "memory_total_bytes": uint64(0), "memory_used_bytes": uint64(0), "available": true, "sampled_at": time.Now().UTC()}
	if f, err := os.Open("/proc/stat"); err == nil {
		s := bufio.NewScanner(f)
		if s.Scan() {
			fields := strings.Fields(s.Text())
			var vals []uint64
			for _, v := range fields[1:] {
				n, _ := strconv.ParseUint(v, 10, 64)
				vals = append(vals, n)
			}
			if len(vals) >= 4 {
				var total uint64
				for _, v := range vals {
					total += v
				}
				idle := vals[3]
				cpuSample.Lock()
				if cpuSample.total > 0 && total > cpuSample.total {
					dt := float64(total - cpuSample.total)
					di := float64(idle - cpuSample.idle)
					m["cpu_percent"] = (dt - di) / dt * 100
				}
				cpuSample.idle = idle
				cpuSample.total = total
				cpuSample.at = time.Now()
				cpuSample.Unlock()
			}
		}
		f.Close()
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		s := bufio.NewScanner(f)
		var total, avail uint64
		for s.Scan() {
			parts := strings.Fields(s.Text())
			if len(parts) < 2 {
				continue
			}
			n, _ := strconv.ParseUint(parts[1], 10, 64)
			switch strings.TrimSuffix(parts[0], ":") {
			case "MemTotal":
				total = n * 1024
			case "MemAvailable":
				avail = n * 1024
			}
		}
		f.Close()
		m["memory_total_bytes"] = total
		if avail <= total {
			m["memory_used_bytes"] = total - avail
		}
	}
	m["cpu_count"] = runtime.NumCPU()
	return m
}
