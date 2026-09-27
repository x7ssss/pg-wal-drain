package cmd

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/x7ssss/pg-wal-drain/pkg/control"
)

func createMockPGData(t *testing.T) (string, string) {
	pgdata := t.TempDir()
	globalDir := filepath.Join(pgdata, "global")
	walDir := filepath.Join(pgdata, "pg_wal")
	archiveStatusDir := filepath.Join(walDir, "archive_status")

	if err := os.MkdirAll(globalDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archiveStatusDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Build mock pg_control
	// Redo LSN: 0x0000000100000003 * 16MB = 0x0000000003000028 -> Cutoff segment 000000010000000000000003
	ctrlBuf := make([]byte, 8192)
	order := binary.LittleEndian
	order.PutUint64(ctrlBuf[0:8], 0x1234567890ABCDEF)
	order.PutUint32(ctrlBuf[8:12], 1300)
	order.PutUint32(ctrlBuf[12:16], 202209061)
	order.PutUint32(ctrlBuf[16:20], uint32(control.DBInCrashRecovery))
	order.PutUint64(ctrlBuf[24:32], uint64(time.Now().Unix()))
	order.PutUint64(ctrlBuf[32:40], 0x0000000003000050)
	// CheckPointCopy at 40
	order.PutUint64(ctrlBuf[40:48], 0x0000000003000028) // Redo LSN in segment 3
	order.PutUint32(ctrlBuf[48:52], 1)                  // Timeline 1
	order.PutUint32(ctrlBuf[52:56], 1)
	ctrlBuf[56] = 1

	// floatFormat at 192, wal_seg_size at 212
	floatOffset := 192
	order.PutUint64(ctrlBuf[floatOffset:floatOffset+8], math.Float64bits(1234567.0))
	order.PutUint32(ctrlBuf[floatOffset+20:floatOffset+24], 16*1024*1024)

	if err := os.WriteFile(filepath.Join(globalDir, "pg_control"), ctrlBuf, 0600); err != nil {
		t.Fatal(err)
	}

	// Create WAL segments:
	// Seg 1: < cutoff, has .done -> SAFE TO EVACUATE
	seg1 := "000000010000000000000001"
	if err := os.WriteFile(filepath.Join(walDir, seg1), make([]byte, 1024), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveStatusDir, seg1+".done"), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	// Seg 2: < cutoff, has .ready -> UNARCHIVED OLDER
	seg2 := "000000010000000000000002"
	if err := os.WriteFile(filepath.Join(walDir, seg2), make([]byte, 1024), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveStatusDir, seg2+".ready"), []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	// Seg 3: == cutoff (redo segment) -> ACTIVE
	seg3 := "000000010000000000000003"
	if err := os.WriteFile(filepath.Join(walDir, seg3), make([]byte, 1024), 0600); err != nil {
		t.Fatal(err)
	}

	// Seg 4: > cutoff -> ACTIVE / FUTURE
	seg4 := "000000010000000000000004"
	if err := os.WriteFile(filepath.Join(walDir, seg4), make([]byte, 1024), 0600); err != nil {
		t.Fatal(err)
	}

	cutoff := "000000010000000000000003"
	return pgdata, cutoff
}

func TestRunOfflineInspectDryRun(t *testing.T) {
	pgdata, _ := createMockPGData(t)
	backupDir := filepath.Join(t.TempDir(), "wal_backup")

	err := runOfflineInspect(pgdata, backupDir, true)
	if err != nil {
		t.Fatalf("runOfflineInspect dry-run failed: %v", err)
	}

	// In dry-run, safe segment should still exist in pg_wal
	seg1Path := filepath.Join(pgdata, "pg_wal", "000000010000000000000001")
	if _, err := os.Stat(seg1Path); os.IsNotExist(err) {
		t.Errorf("expected seg1 to remain in pg_wal during dry-run")
	}
}

func TestRunOfflineInspectRealEvacuation(t *testing.T) {
	pgdata, _ := createMockPGData(t)
	backupDir := filepath.Join(t.TempDir(), "wal_backup")

	err := runOfflineInspect(pgdata, backupDir, false)
	if err != nil {
		t.Fatalf("runOfflineInspect real evacuation failed: %v", err)
	}

	// Safe segment should have moved to backupDir
	targetSeg1 := filepath.Join(backupDir, "000000010000000000000001")
	if _, err := os.Stat(targetSeg1); os.IsNotExist(err) {
		t.Errorf("expected seg1 to be evacuated to %s", targetSeg1)
	}

	// Target .done file should exist
	targetDone := filepath.Join(backupDir, "archive_status", "000000010000000000000001.done")
	if _, err := os.Stat(targetDone); os.IsNotExist(err) {
		t.Errorf("expected target .done file to exist at %s", targetDone)
	}

	// Safe segment should be gone from pg_wal
	origSeg1 := filepath.Join(pgdata, "pg_wal", "000000010000000000000001")
	if _, err := os.Stat(origSeg1); !os.IsNotExist(err) {
		t.Errorf("expected seg1 to be removed from pg_wal")
	}

	// Unarchived seg2 must remain in pg_wal
	seg2 := filepath.Join(pgdata, "pg_wal", "000000010000000000000002")
	if _, err := os.Stat(seg2); os.IsNotExist(err) {
		t.Errorf("unarchived seg2 should not have been moved")
	}

	// Active seg3 and seg4 must remain in pg_wal
	seg3 := filepath.Join(pgdata, "pg_wal", "000000010000000000000003")
	if _, err := os.Stat(seg3); os.IsNotExist(err) {
		t.Errorf("active seg3 should not have been moved")
	}
	seg4 := filepath.Join(pgdata, "pg_wal", "000000010000000000000004")
	if _, err := os.Stat(seg4); os.IsNotExist(err) {
		t.Errorf("active seg4 should not have been moved")
	}
}

func TestRunOfflineInspectInvalidPGData(t *testing.T) {
	err := runOfflineInspect(filepath.Join(t.TempDir(), "nonexistent"), "", false)
	if err == nil {
		t.Errorf("expected error for nonexistent pgdata, got nil")
	}
}
