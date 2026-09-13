//go:build linux || darwin

package rooms

import (
	"os"
	"syscall"
)

func fileDiskUsage(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
		return st.Blocks * 512
	}
	return info.Size()
}
