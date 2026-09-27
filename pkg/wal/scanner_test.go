package wal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsWALSegmentFileName(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"000000010000000000000001", true},
		{"0000000100000001000000FF", true},
		{"FFFFFFFFFFFFFFFFFFFFFFFF", true},
		{"00000002.history", false},
		{"000000010000000000000001.00000028.backup", false},
		{"000000010000000000000001.partial", false},
		{"archive_status", false},
		{"00000001000000000000001", false},  // 23 chars
		{"0000000100000000000000001", false}, // 25 chars
		{"00000001000000000000000G", false}, // invalid hex char
	}

	for _, tc := range tests {
		got := IsWALSegmentFileName(tc.name)
		if got != tc.expected {
			t.Errorf("IsWALSegmentFileName(%q) = %v, expected %v", tc.name, got, tc.expected)
		}
	}
}

func setupMockWAL(t *testing.T) (string, string) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	archiveStatus := filepath.Join(walDir, "archive_status")
	if err := os.MkdirAll(archiveStatus, 0750); err != nil {
		t.Fatalf("failed to create mock archive_status: %v", err)
	}

	// 1. Safe to evacuate: < cutoff, has .done
	seg1 := "000000010000000000000001"
	if err := os.WriteFile(filepath.Join(walDir, seg1), make([]byte, 1024), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveStatus, seg1+".done"), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	// 2. Unarchived older: < cutoff, has .ready
	seg2 := "000000010000000000000002"
	if err := os.WriteFile(filepath.Join(walDir, seg2), make([]byte, 2048), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveStatus, seg2+".ready"), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	// 3. Unarchived older: < cutoff, no status
	seg3 := "000000010000000000000003"
	if err := os.WriteFile(filepath.Join(walDir, seg3), make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}

	// 4. Active cutoff: == cutoff
	seg4 := "000000010000000000000004"
	if err := os.WriteFile(filepath.Join(walDir, seg4), make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}

	// 5. Future segment: > cutoff
	seg5 := "000000010000000000000005"
	if err := os.WriteFile(filepath.Join(walDir, seg5), make([]byte, 16384), 0600); err != nil {
		t.Fatal(err)
	}

	// Non-WAL file: .history
	if err := os.WriteFile(filepath.Join(walDir, "00000001.history"), []byte("history"), 0600); err != nil {
		t.Fatal(err)
	}

	cutoff := "000000010000000000000004"
	return walDir, cutoff
}

func TestScanWALDirectory(t *testing.T) {
	walDir, cutoff := setupMockWAL(t)

	report, err := ScanWALDirectory(walDir, cutoff)
	if err != nil {
		t.Fatalf("ScanWALDirectory failed: %v", err)
	}

	if report.TotalSegments != 5 {
		t.Errorf("TotalSegments = %d, expected 5", report.TotalSegments)
	}

	if len(report.SafeSegments) != 1 {
		t.Fatalf("len(SafeSegments) = %d, expected 1", len(report.SafeSegments))
	}
	if report.SafeSegments[0].Name != "000000010000000000000001" {
		t.Errorf("SafeSegments[0].Name = %s, expected 000000010000000000000001", report.SafeSegments[0].Name)
	}
	if report.SafeBytes != 1024 {
		t.Errorf("SafeBytes = %d, expected 1024", report.SafeBytes)
	}

	if len(report.UnarchivedOlder) != 2 {
		t.Fatalf("len(UnarchivedOlder) = %d, expected 2", len(report.UnarchivedOlder))
	}
	if report.UnarchivedOlder[0].Name != "000000010000000000000002" || report.UnarchivedOlder[1].Name != "000000010000000000000003" {
		t.Errorf("UnarchivedOlder unexpected names: %s, %s", report.UnarchivedOlder[0].Name, report.UnarchivedOlder[1].Name)
	}

	if len(report.ActiveSegments) != 2 {
		t.Fatalf("len(ActiveSegments) = %d, expected 2", len(report.ActiveSegments))
	}
	if report.ActiveSegments[0].Name != "000000010000000000000004" || report.ActiveSegments[1].Name != "000000010000000000000005" {
		t.Errorf("ActiveSegments unexpected names: %s, %s", report.ActiveSegments[0].Name, report.ActiveSegments[1].Name)
	}
}

func TestEvacuateSafeSegments(t *testing.T) {
	walDir, cutoff := setupMockWAL(t)
	targetDir := filepath.Join(t.TempDir(), "wal_backup")

	report, err := ScanWALDirectory(walDir, cutoff)
	if err != nil {
		t.Fatalf("ScanWALDirectory failed: %v", err)
	}

	// 1. Dry run
	dryRunResult, err := EvacuateSafeSegments(report, EvacuateOptions{
		TargetDir: targetDir,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if dryRunResult.EvacuatedCount != 1 {
		t.Errorf("dryRun evacuated count = %d, expected 1", dryRunResult.EvacuatedCount)
	}
	// Check that file still exists in source
	if !fileExists(filepath.Join(walDir, "000000010000000000000001")) {
		t.Errorf("dry run should not have removed source file")
	}

	// 2. Real evacuation
	realResult, err := EvacuateSafeSegments(report, EvacuateOptions{
		TargetDir: targetDir,
		DryRun:    false,
	})
	if err != nil {
		t.Fatalf("real evacuation failed: %v", err)
	}
	if realResult.EvacuatedCount != 1 {
		t.Errorf("real evacuation count = %d, expected 1", realResult.EvacuatedCount)
	}
	if realResult.EvacuatedBytes != 1024 {
		t.Errorf("real evacuation bytes = %d, expected 1024", realResult.EvacuatedBytes)
	}

	// Verify evacuated file exists in target
	targetFile := filepath.Join(targetDir, "000000010000000000000001")
	if !fileExists(targetFile) {
		t.Errorf("evacuated file does not exist at %s", targetFile)
	}

	// Verify target .done file exists
	targetDone := filepath.Join(targetDir, "archive_status", "000000010000000000000001.done")
	if !fileExists(targetDone) {
		t.Errorf("evacuated .done file does not exist at %s", targetDone)
	}

	// Verify source safe segment is removed from pg_wal
	sourceFile := filepath.Join(walDir, "000000010000000000000001")
	if fileExists(sourceFile) {
		t.Errorf("source file %s was not removed", sourceFile)
	}

	// Verify unarchived older and active files were NOT moved
	if !fileExists(filepath.Join(walDir, "000000010000000000000002")) {
		t.Errorf("unarchived segment was improperly removed")
	}
	if !fileExists(filepath.Join(walDir, "000000010000000000000004")) {
		t.Errorf("active segment was improperly removed")
	}
}

func TestEvacuateSafeSegmentsErrors(t *testing.T) {
	walDir, cutoff := setupMockWAL(t)
	report, err := ScanWALDirectory(walDir, cutoff)
	if err != nil {
		t.Fatal(err)
	}

	// Empty target
	_, err = EvacuateSafeSegments(report, EvacuateOptions{TargetDir: ""})
	if err == nil {
		t.Errorf("expected error for empty target dir, got nil")
	}

	// Target equals walDir
	_, err = EvacuateSafeSegments(report, EvacuateOptions{TargetDir: walDir})
	if err == nil {
		t.Errorf("expected error for target equals walDir, got nil")
	}
}
