package control

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"time"
)

// DBState represents the cluster state in pg_control.
type DBState uint32

const (
	DBStartup              DBState = 0
	DBShutdowned           DBState = 1
	DBShutdownedInRecovery DBState = 2
	DBShutdowning          DBState = 3
	DBInCrashRecovery      DBState = 4
	DBInArchiveRecovery    DBState = 5
	DBInProduction         DBState = 6
)

func (s DBState) String() string {
	switch s {
	case DBStartup:
		return "starting up"
	case DBShutdowned:
		return "shut down"
	case DBShutdownedInRecovery:
		return "shut down in recovery"
	case DBShutdowning:
		return "shutting down"
	case DBInCrashRecovery:
		return "in crash recovery"
	case DBInArchiveRecovery:
		return "in archive recovery"
	case DBInProduction:
		return "in production"
	default:
		return fmt.Sprintf("unknown (%d)", s)
	}
}

// CheckPoint represents the checkpoint metadata inside ControlFileData.
type CheckPoint struct {
	Redo           uint64 // Redo LSN where crash recovery starts
	ThisTimeLineID uint32 // Current timeline ID
	PrevTimeLineID uint32 // Previous timeline ID
	FullPageWrites bool
}

// ControlFileData represents the parsed content of global/pg_control.
type ControlFileData struct {
	SystemIdentifier uint64
	PGControlVersion uint32
	CatalogVersionNo uint32
	State            DBState
	Time             time.Time
	CheckPointLSN    uint64
	CheckPointCopy   CheckPoint
	WALSegSize       uint64
	EarliestSafeSeg  string // Active redo segment cutoff (%08X%08X%08X)
}

// FormatWALSegment formats timeline, LSN, and segment size into standard 24-character WAL filename.
// In PostgreSQL:
// segNo = redoLSN / segSize
// segsPerId = 0x100000000 / segSize
// logId = segNo / segsPerId (equivalent to redoLSN >> 32)
// segId = segNo % segsPerId (equivalent to (redoLSN & 0xFFFFFFFF) / segSize)
// result format: %08X%08X%08X (timeline, logId, segId)
func FormatWALSegment(timeline uint32, redoLSN uint64, segSize uint64) string {
	if segSize == 0 {
		segSize = 16 * 1024 * 1024
	}
	segNo := redoLSN / segSize
	segsPerId := uint64(0x100000000) / segSize
	logId := uint32(segNo / segsPerId)
	segId := uint32(segNo % segsPerId)
	return fmt.Sprintf("%08X%08X%08X", timeline, logId, segId)
}

// FormatLSN formats uint64 LSN into PostgreSQL standard string representation X/Y.
func FormatLSN(lsn uint64) string {
	return fmt.Sprintf("%X/%X", uint32(lsn>>32), uint32(lsn&0xFFFFFFFF))
}

// ParseLSN parses PostgreSQL standard string representation X/Y into uint64.
func ParseLSN(s string) (uint64, error) {
	var hi, lo uint32
	n, err := fmt.Sscanf(s, "%X/%X", &hi, &lo)
	if err != nil || n != 2 {
		return 0, fmt.Errorf("invalid LSN format: %s", s)
	}
	return (uint64(hi) << 32) | uint64(lo), nil
}

// ParseControlFile reads and parses global/pg_control from the given file path.
func ParseControlFile(filePath string) (*ControlFileData, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read pg_control at %s: %w", filePath, err)
	}
	return ParseControlData(data)
}

// ParseControlData parses raw bytes of global/pg_control.
func ParseControlData(data []byte) (*ControlFileData, error) {
	if len(data) < 512 {
		return nil, errors.New("pg_control data is too short, minimum size is 512 bytes")
	}

	// Detect endianness using pg_control_version (offset 8..12).
	// PostgreSQL 13+ versions use version numbers >= 1000 and <= 4000 (e.g. 1300, 2001).
	var order binary.ByteOrder
	leVer := binary.LittleEndian.Uint32(data[8:12])
	beVer := binary.BigEndian.Uint32(data[8:12])

	if leVer >= 1000 && leVer <= 4000 {
		order = binary.LittleEndian
	} else if beVer >= 1000 && beVer <= 4000 {
		order = binary.BigEndian
	} else {
		return nil, fmt.Errorf("unsupported or invalid pg_control version (LE=%d, BE=%d)", leVer, beVer)
	}

	cf := &ControlFileData{
		SystemIdentifier: order.Uint64(data[0:8]),
		PGControlVersion: order.Uint32(data[8:12]),
		CatalogVersionNo: order.Uint32(data[12:16]),
		State:            DBState(order.Uint32(data[16:20])),
		Time:             time.Unix(int64(order.Uint64(data[24:32])), 0),
		CheckPointLSN:    order.Uint64(data[32:40]),
		CheckPointCopy: CheckPoint{
			Redo:           order.Uint64(data[40:48]),
			ThisTimeLineID: order.Uint32(data[48:52]),
			PrevTimeLineID: order.Uint32(data[52:56]),
			FullPageWrites: data[56] != 0,
		},
		WALSegSize: 16 * 1024 * 1024, // Standard default 16MB
	}

	// Extract wal_seg_size by locating floatFormat (1234567.0).
	var floatBytes [8]byte
	order.PutUint64(floatBytes[:], math.Float64bits(1234567.0))
	idx := bytes.Index(data, floatBytes[:])
	if idx != -1 && idx+24 <= len(data) {
		candSegSize := uint64(order.Uint32(data[idx+20 : idx+24]))
		// Validate that candSegSize is a power of 2 between 1MB and 1GB.
		if candSegSize >= 1024*1024 && candSegSize <= 1024*1024*1024 && (candSegSize&(candSegSize-1)) == 0 {
			cf.WALSegSize = candSegSize
		}
	}

	cf.EarliestSafeSeg = FormatWALSegment(cf.CheckPointCopy.ThisTimeLineID, cf.CheckPointCopy.Redo, cf.WALSegSize)
	return cf, nil
}
