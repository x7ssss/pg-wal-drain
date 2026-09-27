//go:build !windows

package fs

import (
	"syscall"
)

// Statfs queries the filesystem space metrics via syscall.Statfs.
func Statfs(path string) (DiskUsage, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return DiskUsage{}, err
	}

	bsize := uint64(stat.Bsize)
	total := uint64(stat.Blocks) * bsize
	free := uint64(stat.Bfree) * bsize
	avail := uint64(stat.Bavail) * bsize
	used := total - free

	var usedPct float64
	if total > 0 {
		usedPct = (float64(used) / float64(total)) * 100.0
	}

	return DiskUsage{
		Path:       path,
		TotalBytes: total,
		FreeBytes:  free,
		AvailBytes: avail,
		UsedBytes:  used,
		UsedPct:    usedPct,
	}, nil
}
