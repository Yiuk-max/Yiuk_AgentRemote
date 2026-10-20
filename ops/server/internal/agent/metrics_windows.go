//go:build windows

package agent

import (
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	getSystemTimes       = kernel32.NewProc("GetSystemTimes")
	globalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

type fileTime struct{ Low, High uint32 }

func (f fileTime) value() uint64 { return uint64(f.High)<<32 | uint64(f.Low) }

type memoryStatus struct {
	Length, Load                                               uint32
	TotalPhys, AvailablePhys, TotalPageFile, AvailablePageFile uint64
	TotalVirtual, AvailableVirtual, AvailableExtendedVirtual   uint64
}

var winCPU struct {
	sync.Mutex
	idle, total uint64
}

func systemMetrics() map[string]any {
	result := map[string]any{"available": true, "cpu_count": runtime.NumCPU(), "cpu_percent": 0.0, "sampled_at": time.Now().UTC()}
	var idle, kernel, user fileTime
	if r, _, _ := getSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); r != 0 {
		i := idle.value()
		t := kernel.value() + user.value()
		winCPU.Lock()
		if winCPU.total > 0 && t > winCPU.total {
			dt := float64(t - winCPU.total)
			di := float64(i - winCPU.idle)
			result["cpu_percent"] = (dt - di) / dt * 100
		}
		winCPU.idle = i
		winCPU.total = t
		winCPU.Unlock()
	}
	mem := memoryStatus{Length: uint32(unsafe.Sizeof(memoryStatus{}))}
	if r, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem))); r != 0 {
		result["memory_total_bytes"] = mem.TotalPhys
		result["memory_used_bytes"] = mem.TotalPhys - mem.AvailablePhys
	} else {
		result["available"] = false
	}
	return result
}
