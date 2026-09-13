//go:build !linux && !darwin

package rooms

import "os"

func fileDiskUsage(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}
