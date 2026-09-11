//go:build !linux

package metrics

import (
	"runtime"

	"golang.org/x/sys/windows"
)

func diskUsage(path string) (total, used, free uint64, pct float64) {
	// Local-trial support: real disk numbers on Windows (production is Linux).
	// Other non-Linux platforms keep returning zeros.
	if runtime.GOOS != "windows" {
		return 0, 0, 0, 0
	}
	var avail, totalBytes, totalFree uint64
	dir, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, 0
	}
	if err := windows.GetDiskFreeSpaceEx(dir, &avail, &totalBytes, &totalFree); err != nil {
		return 0, 0, 0, 0
	}
	total = totalBytes
	free = avail
	if total >= free {
		used = total - free
	}
	den := used + free
	if den > 0 {
		pct = float64(used) / float64(den) * 100
	}
	return
}

