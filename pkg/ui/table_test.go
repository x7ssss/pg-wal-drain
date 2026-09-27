package ui

import (
	"strings"
	"testing"
)

func TestTableRender(t *testing.T) {
	table := NewTable("REPLICATION SLOTS AUDIT", "SLOT NAME", "TYPE", "ACTIVE", "LAG BYTES")
	table.SetAlignment(AlignLeft, AlignLeft, AlignCenter, AlignRight)
	table.AddRow("sub_sales", "logical", "true", "12.5 MB")
	table.AddRow("sub_analytics_stale", "logical", "false", "14.2 GB")

	output := table.Render()

	if !strings.Contains(output, "REPLICATION SLOTS AUDIT") {
		t.Errorf("output missing table title")
	}
	if !strings.Contains(output, "sub_sales") || !strings.Contains(output, "sub_analytics_stale") {
		t.Errorf("output missing expected rows")
	}
	if !strings.Contains(output, "+") || !strings.Contains(output, "|") {
		t.Errorf("output missing expected borders")
	}
}

func TestBannerRender(t *testing.T) {
	items := [][2]string{
		{"CLUSTER STATE", "in crash recovery"},
		{"ACTIVE REDO CUTOFF", "000000010000000000000004"},
		{"DISK USAGE", "94.20% [CRITICAL]"},
	}

	banner := Banner("PostgreSQL Emergency WAL Diagnostic", items)
	if !strings.Contains(banner, "POSTGRESQL EMERGENCY WAL DIAGNOSTIC") {
		t.Errorf("banner missing title")
	}
	if !strings.Contains(banner, "in crash recovery") {
		t.Errorf("banner missing item value")
	}
}

func TestAlertBox(t *testing.T) {
	alert := AlertBox("CRITICAL", "Disk exhaustion detected", "Free space is below 5%", "Immediate action required")
	if !strings.Contains(alert, "[CRITICAL]") {
		t.Errorf("alert missing level tag")
	}
	if !strings.Contains(alert, "Disk exhaustion detected") {
		t.Errorf("alert missing message")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{-10, "0 B"},
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
	}

	for _, tc := range tests {
		got := FormatBytes(tc.bytes)
		if got != tc.expected {
			t.Errorf("FormatBytes(%d) = %q, expected %q", tc.bytes, got, tc.expected)
		}
	}
}
