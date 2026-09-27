//go:build windows

package fs

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// Statfs queries the filesystem space metrics on Windows using GetDiskFreeSpaceExW.
func Statfs(path string) (DiskUsage, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return DiskUsage{}, err
	}

	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	r1, _, callErr := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if r1 == 0 {
		return DiskUsage{}, fmt.Errorf("GetDiskFreeSpaceEx failed: %w", callErr)
	}

	used := totalNumberOfBytes - totalNumberOfFreeBytes
	var usedPct float64
	if totalNumberOfBytes > 0 {
		usedPct = (float64(used) / float64(totalNumberOfBytes)) * 100.0
	}

	return DiskUsage{
		Path:       path,
		TotalBytes: totalNumberOfBytes,
		FreeBytes:  totalNumberOfFreeBytes,
		AvailBytes: freeBytesAvailable,
		UsedBytes:  used,
		UsedPct:    usedPct,
	}, nil
}
