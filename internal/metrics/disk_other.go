//go:build !linux

package metrics

func diskUsage(path string) (total, used, free uint64, pct float64) {
	return 0, 0, 0, 0
}
