package fs

import "fmt"

// DiskUsage holds filesystem space metrics.
type DiskUsage struct {
	Path       string  `json:"path"`
	TotalBytes uint64  `json:"total_bytes"`
	FreeBytes  uint64  `json:"free_bytes"`
	AvailBytes uint64  `json:"avail_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// GetDiskUsage inspects the filesystem containing the given path.
func GetDiskUsage(path string) (DiskUsage, error) {
	return Statfs(path)
}

// FormatBytes formats a byte count into a human-readable string.
func FormatBytes(b uint64) string {
	const (
		unitKB = 1024
		unitMB = 1024 * unitKB
		unitGB = 1024 * unitMB
		unitTB = 1024 * unitGB
	)

	switch {
	case b >= unitTB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(unitTB))
	case b >= unitGB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(unitGB))
	case b >= unitMB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(unitMB))
	case b >= unitKB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(unitKB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
