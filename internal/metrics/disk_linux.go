//go:build linux

package metrics

import (
	"syscall"
)

func diskUsage(path string) (total, used, free uint64, pct float64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return
	}
	bsize := uint64(st.Bsize)
	if st.Frsize > 0 {
		bsize = uint64(st.Frsize)
	}
	total = st.Blocks * bsize
	free = st.Bavail * bsize
	if st.Blocks >= st.Bfree {
		used = (st.Blocks - st.Bfree) * bsize
	}
	// Match `df` Use%: used / (used + available)
	den := used + free
	if den > 0 {
		pct = float64(used) / float64(den) * 100
	}
	return
}
