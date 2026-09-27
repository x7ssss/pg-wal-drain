package control

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func buildMockControlFile(
	sysID uint64,
	version uint32,
	catVer uint32,
	state DBState,
	tUnix int64,
	cpLSN uint64,
	redoLSN uint64,
	tli uint32,
	prevTLI uint32,
	fpw bool,
	walSegSize uint32,
) []byte {
	buf := make([]byte, 8192)
	order := binary.LittleEndian

	order.PutUint64(buf[0:8], sysID)
	order.PutUint32(buf[8:12], version)
	order.PutUint32(buf[12:16], catVer)
	order.PutUint32(buf[16:20], uint32(state))
	// 20..24 padding
	order.PutUint64(buf[24:32], uint64(tUnix))
	order.PutUint64(buf[32:40], cpLSN)

	// CheckPointCopy starts at offset 40
	order.PutUint64(buf[40:48], redoLSN)
	order.PutUint32(buf[48:52], tli)
	order.PutUint32(buf[52:56], prevTLI)
	if fpw {
		buf[56] = 1
	} else {
		buf[56] = 0
	}

	// Place floatFormat (1234567.0) at offset 192 and wal_seg_size at offset 212
	floatOffset := 192
	order.PutUint64(buf[floatOffset:floatOffset+8], math.Float64bits(1234567.0))
	// blcksz at floatOffset+8
	order.PutUint32(buf[floatOffset+8:floatOffset+12], 8192)
	// relseg_size at floatOffset+12
	order.PutUint32(buf[floatOffset+12:floatOffset+16], 131072)
	// xlog_blcksz at floatOffset+16
	order.PutUint32(buf[floatOffset+16:floatOffset+20], 8192)
	// xlog_seg_size at floatOffset+20
	order.PutUint32(buf[floatOffset+20:floatOffset+24], walSegSize)

	return buf
}

func TestFormatWALSegment(t *testing.T) {
	tests := []struct {
		name     string
		timeline uint32
		redoLSN  uint64
		segSize  uint64
		expected string
	}{
		{
			name:     "Timeline 1, redo in first segment",
			timeline: 1,
			redoLSN:  0x0000000000000028,
			segSize:  16 * 1024 * 1024,
			expected: "000000010000000000000000",
		},
		{
			name:     "Timeline 1, redo in segment 1",
			timeline: 1,
			redoLSN:  0x0000000001000028,
			segSize:  16 * 1024 * 1024,
			expected: "000000010000000000000001",
		},
		{
			name:     "Timeline 1, redo crossing log boundary (LSN 1/0)",
			timeline: 1,
			redoLSN:  0x0000000100000000,
			segSize:  16 * 1024 * 1024,
			expected: "000000010000000100000000",
		},
		{
			name:     "Timeline 2, LSN 1/01000000",
			timeline: 2,
			redoLSN:  0x0000000101000000,
			segSize:  16 * 1024 * 1024,
			expected: "000000020000000100000001",
		},
		{
			name:     "Timeline 1, custom 64MB segment size",
			timeline: 1,
			redoLSN:  0x0000000004000028,
			segSize:  64 * 1024 * 1024,
			expected: "000000010000000000000001",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatWALSegment(tc.timeline, tc.redoLSN, tc.segSize)
			if got != tc.expected {
				t.Errorf("FormatWALSegment() = %s, expected %s", got, tc.expected)
			}
		})
	}
}

func TestFormatAndParseLSN(t *testing.T) {
	lsn := uint64(0x0000000102000050)
	formatted := FormatLSN(lsn)
	expectedStr := "1/2000050"
	if formatted != expectedStr {
		t.Errorf("FormatLSN() = %s, expected %s", formatted, expectedStr)
	}

	parsed, err := ParseLSN(formatted)
	if err != nil {
		t.Fatalf("ParseLSN(%s) error: %v", formatted, err)
	}
	if parsed != lsn {
		t.Errorf("ParseLSN() = %X, expected %X", parsed, lsn)
	}

	// Test invalid LSN format
	_, err = ParseLSN("invalid_lsn")
	if err == nil {
		t.Errorf("expected error for invalid LSN, got nil")
	}
}

func TestParseControlData(t *testing.T) {
	sysID := uint64(0x7123456789ABCDEF)
	version := uint32(1300)
	catVer := uint32(202209061)
	state := DBInCrashRecovery
	tUnix := int64(1700000000)
	cpLSN := uint64(0x0000000102000050)
	redoLSN := uint64(0x0000000102000028)
	tli := uint32(1)
	prevTLI := uint32(1)
	fpw := true
	segSize := uint32(16 * 1024 * 1024)

	raw := buildMockControlFile(sysID, version, catVer, state, tUnix, cpLSN, redoLSN, tli, prevTLI, fpw, segSize)

	cf, err := ParseControlData(raw)
	if err != nil {
		t.Fatalf("ParseControlData failed: %v", err)
	}

	if cf.SystemIdentifier != sysID {
		t.Errorf("SystemIdentifier = %X, expected %X", cf.SystemIdentifier, sysID)
	}
	if cf.PGControlVersion != version {
		t.Errorf("PGControlVersion = %d, expected %d", cf.PGControlVersion, version)
	}
	if cf.CatalogVersionNo != catVer {
		t.Errorf("CatalogVersionNo = %d, expected %d", cf.CatalogVersionNo, catVer)
	}
	if cf.State != state {
		t.Errorf("State = %v, expected %v", cf.State, state)
	}
	if cf.CheckPointLSN != cpLSN {
		t.Errorf("CheckPointLSN = %X, expected %X", cf.CheckPointLSN, cpLSN)
	}
	if cf.CheckPointCopy.Redo != redoLSN {
		t.Errorf("Redo = %X, expected %X", cf.CheckPointCopy.Redo, redoLSN)
	}
	if cf.CheckPointCopy.ThisTimeLineID != tli {
		t.Errorf("ThisTimeLineID = %d, expected %d", cf.CheckPointCopy.ThisTimeLineID, tli)
	}
	if cf.CheckPointCopy.PrevTimeLineID != prevTLI {
		t.Errorf("PrevTimeLineID = %d, expected %d", cf.CheckPointCopy.PrevTimeLineID, prevTLI)
	}
	if !cf.CheckPointCopy.FullPageWrites {
		t.Errorf("expected FullPageWrites = true, got false")
	}
	if cf.WALSegSize != uint64(segSize) {
		t.Errorf("WALSegSize = %d, expected %d", cf.WALSegSize, segSize)
	}

	expectedCutoff := FormatWALSegment(tli, redoLSN, uint64(segSize))
	if cf.EarliestSafeSeg != expectedCutoff {
		t.Errorf("EarliestSafeSeg = %s, expected %s", cf.EarliestSafeSeg, expectedCutoff)
	}
}

func TestParseControlFile(t *testing.T) {
	tempDir := t.TempDir()
	ctrlPath := filepath.Join(tempDir, "pg_control")

	raw := buildMockControlFile(
		12345,
		1300,
		20210000,
		DBInProduction,
		time.Now().Unix(),
		0x01000028,
		0x01000000,
		1,
		1,
		false,
		16*1024*1024,
	)

	if err := os.WriteFile(ctrlPath, raw, 0600); err != nil {
		t.Fatalf("failed to write mock pg_control: %v", err)
	}

	cf, err := ParseControlFile(ctrlPath)
	if err != nil {
		t.Fatalf("ParseControlFile failed: %v", err)
	}

	if cf.State != DBInProduction {
		t.Errorf("State = %v, expected DBInProduction", cf.State)
	}
	if cf.EarliestSafeSeg != "000000010000000000000001" {
		t.Errorf("EarliestSafeSeg = %s, expected 000000010000000000000001", cf.EarliestSafeSeg)
	}
}

func TestParseControlDataErrors(t *testing.T) {
	// Buffer too small
	_, err := ParseControlData(make([]byte, 100))
	if err == nil {
		t.Errorf("expected error for short buffer, got nil")
	}

	// Invalid version
	raw := make([]byte, 8192)
	binary.LittleEndian.PutUint32(raw[8:12], 99) // Invalid version
	_, err = ParseControlData(raw)
	if err == nil {
		t.Errorf("expected error for invalid version, got nil")
	}
}

func TestDBStateString(t *testing.T) {
	tests := []struct {
		state    DBState
		expected string
	}{
		{DBStartup, "starting up"},
		{DBShutdowned, "shut down"},
		{DBShutdownedInRecovery, "shut down in recovery"},
		{DBShutdowning, "shutting down"},
		{DBInCrashRecovery, "in crash recovery"},
		{DBInArchiveRecovery, "in archive recovery"},
		{DBInProduction, "in production"},
		{DBState(999), "unknown (999)"},
	}

	for _, tc := range tests {
		if tc.state.String() != tc.expected {
			t.Errorf("DBState(%d).String() = %q, expected %q", tc.state, tc.state.String(), tc.expected)
		}
	}
}
