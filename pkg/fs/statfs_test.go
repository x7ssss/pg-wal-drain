package fs

import (
	"os"
	"testing"
)

func TestGetDiskUsage(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error: %v", err)
	}

	usage, err := GetDiskUsage(wd)
	if err != nil {
		t.Fatalf("GetDiskUsage(%q) error: %v", wd, err)
	}

	if usage.TotalBytes == 0 {
		t.Errorf("expected TotalBytes > 0, got 0")
	}

	if usage.UsedPct < 0.0 || usage.UsedPct > 100.0 {
		t.Errorf("expected UsedPct between 0 and 100, got %f", usage.UsedPct)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    uint64
		expected string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
		{1099511627776, "1.00 TB"},
	}

	for _, tc := range tests {
		res := FormatBytes(tc.bytes)
		if res != tc.expected {
			t.Errorf("FormatBytes(%d) = %q, expected %q", tc.bytes, res, tc.expected)
		}
	}
}
