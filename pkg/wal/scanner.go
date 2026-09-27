package wal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var walFileNameRegex = regexp.MustCompile(`^[0-9A-F]{24}$`)

// IsWALSegmentFileName checks if filename matches standard 24-character hex WAL segment naming.
func IsWALSegmentFileName(name string) bool {
	return walFileNameRegex.MatchString(name)
}

// Classification indicates the safety status of a WAL segment.
type Classification int

const (
	ClassificationSafeToEvacuate Classification = iota // < activeRedoCutoff AND .done exists
	ClassificationUnarchivedOlder                       // < activeRedoCutoff AND NO .done
	ClassificationActiveOrFuture                        // >= activeRedoCutoff (critical for recovery)
)

func (c Classification) String() string {
	switch c {
	case ClassificationSafeToEvacuate:
		return "SAFE_TO_EVACUATE"
	case ClassificationUnarchivedOlder:
		return "UNARCHIVED_OLDER"
	case ClassificationActiveOrFuture:
		return "ACTIVE_OR_FUTURE"
	default:
		return "UNKNOWN"
	}
}

// SegmentInfo contains metadata and classification for an individual WAL file.
type SegmentInfo struct {
	Name           string         `json:"name"`
	Path           string         `json:"path"`
	Size           int64          `json:"size"`
	ModTime        time.Time      `json:"mod_time"`
	Classification Classification `json:"classification"`
	HasDone        bool           `json:"has_done"`
	HasReady       bool           `json:"has_ready"`
}

// ScanReport aggregates WAL directory inspection metrics.
type ScanReport struct {
	WALDir           string        `json:"wal_dir"`
	ActiveRedoCutoff string        `json:"active_redo_cutoff"`
	TotalSegments    int           `json:"total_segments"`
	TotalBytes       int64         `json:"total_bytes"`
	SafeSegments     []SegmentInfo `json:"safe_segments"`
	SafeBytes        int64         `json:"safe_bytes"`
	UnarchivedOlder  []SegmentInfo `json:"unarchived_older"`
	UnarchivedBytes  int64         `json:"unarchived_bytes"`
	ActiveSegments   []SegmentInfo `json:"active_segments"`
	ActiveBytes      int64         `json:"active_bytes"`
	AllSegments      []SegmentInfo `json:"all_segments"`
}

// ScanWALDirectory scans the given pg_wal directory and categorizes segments relative to activeRedoCutoff.
func ScanWALDirectory(walDir string, activeRedoCutoff string) (*ScanReport, error) {
	fi, err := os.Stat(walDir)
	if err != nil {
		return nil, fmt.Errorf("cannot access wal directory %s: %w", walDir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("wal path %s is not a directory", walDir)
	}

	archiveStatusDir := filepath.Join(walDir, "archive_status")

	entries, err := os.ReadDir(walDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read wal directory %s: %w", walDir, err)
	}

	report := &ScanReport{
		WALDir:           walDir,
		ActiveRedoCutoff: activeRedoCutoff,
		SafeSegments:     make([]SegmentInfo, 0),
		UnarchivedOlder:  make([]SegmentInfo, 0),
		ActiveSegments:   make([]SegmentInfo, 0),
		AllSegments:      make([]SegmentInfo, 0),
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !IsWALSegmentFileName(name) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		donePath := filepath.Join(archiveStatusDir, name+".done")
		readyPath := filepath.Join(archiveStatusDir, name+".ready")

		hasDone := fileExists(donePath)
		hasReady := fileExists(readyPath)

		var class Classification
		if name < activeRedoCutoff {
			if hasDone {
				class = ClassificationSafeToEvacuate
			} else {
				class = ClassificationUnarchivedOlder
			}
		} else {
			class = ClassificationActiveOrFuture
		}

		seg := SegmentInfo{
			Name:           name,
			Path:           filepath.Join(walDir, name),
			Size:           info.Size(),
			ModTime:        info.ModTime(),
			Classification: class,
			HasDone:        hasDone,
			HasReady:       hasReady,
		}

		report.AllSegments = append(report.AllSegments, seg)
		report.TotalSegments++
		report.TotalBytes += seg.Size

		switch class {
		case ClassificationSafeToEvacuate:
			report.SafeSegments = append(report.SafeSegments, seg)
			report.SafeBytes += seg.Size
		case ClassificationUnarchivedOlder:
			report.UnarchivedOlder = append(report.UnarchivedOlder, seg)
			report.UnarchivedBytes += seg.Size
		case ClassificationActiveOrFuture:
			report.ActiveSegments = append(report.ActiveSegments, seg)
			report.ActiveBytes += seg.Size
		}
	}

	// Sort segments in lexicographical order (oldest to newest)
	sort.Slice(report.AllSegments, func(i, j int) bool {
		return report.AllSegments[i].Name < report.AllSegments[j].Name
	})
	sort.Slice(report.SafeSegments, func(i, j int) bool {
		return report.SafeSegments[i].Name < report.SafeSegments[j].Name
	})
	sort.Slice(report.UnarchivedOlder, func(i, j int) bool {
		return report.UnarchivedOlder[i].Name < report.UnarchivedOlder[j].Name
	})
	sort.Slice(report.ActiveSegments, func(i, j int) bool {
		return report.ActiveSegments[i].Name < report.ActiveSegments[j].Name
	})

	return report, nil
}

// EvacuateOptions defines configuration for evacuating safe WAL segments.
type EvacuateOptions struct {
	TargetDir string
	DryRun    bool
}

// EvacuationResult captures details of the evacuation procedure.
type EvacuationResult struct {
	TargetDir      string   `json:"target_dir"`
	EvacuatedCount int      `json:"evacuated_count"`
	EvacuatedBytes int64    `json:"evacuated_bytes"`
	EvacuatedFiles []string `json:"evacuated_files"`
	Errors         []string `json:"errors"`
	DryRun         bool     `json:"dry_run"`
}

// EvacuateSafeSegments moves expendable segments to the target directory.
func EvacuateSafeSegments(report *ScanReport, opts EvacuateOptions) (*EvacuationResult, error) {
	if opts.TargetDir == "" {
		return nil, errors.New("target directory cannot be empty")
	}

	cleanTarget, err := filepath.Abs(opts.TargetDir)
	if err != nil {
		return nil, fmt.Errorf("invalid target directory %s: %w", opts.TargetDir, err)
	}

	cleanWALDir, err := filepath.Abs(report.WALDir)
	if err != nil {
		return nil, fmt.Errorf("invalid wal directory %s: %w", report.WALDir, err)
	}

	if cleanTarget == cleanWALDir {
		return nil, errors.New("target directory cannot be the same as wal directory")
	}

	res := &EvacuationResult{
		TargetDir:      cleanTarget,
		EvacuatedFiles: make([]string, 0),
		Errors:         make([]string, 0),
		DryRun:         opts.DryRun,
	}

	if len(report.SafeSegments) == 0 {
		return res, nil
	}

	if opts.DryRun {
		for _, seg := range report.SafeSegments {
			res.EvacuatedCount++
			res.EvacuatedBytes += seg.Size
			res.EvacuatedFiles = append(res.EvacuatedFiles, seg.Name)
		}
		return res, nil
	}

	targetArchiveStatus := filepath.Join(cleanTarget, "archive_status")
	if err := os.MkdirAll(targetArchiveStatus, 0750); err != nil {
		return nil, fmt.Errorf("failed to create target archive_status directory: %w", err)
	}

	for _, seg := range report.SafeSegments {
		// Strict invariant: segment MUST be strictly less than activeRedoCutoff and have .done
		if seg.Name >= report.ActiveRedoCutoff || !seg.HasDone {
			res.Errors = append(res.Errors, fmt.Sprintf("safety violation: refusing to evacuate %s", seg.Name))
			continue
		}

		targetSegPath := filepath.Join(cleanTarget, seg.Name)
		if err := moveFile(seg.Path, targetSegPath); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("failed to move %s: %v", seg.Name, err))
			continue
		}

		// Also move archive status .done file
		srcDone := filepath.Join(report.WALDir, "archive_status", seg.Name+".done")
		if fileExists(srcDone) {
			dstDone := filepath.Join(targetArchiveStatus, seg.Name+".done")
			_ = moveFile(srcDone, dstDone)
		}

		res.EvacuatedCount++
		res.EvacuatedBytes += seg.Size
		res.EvacuatedFiles = append(res.EvacuatedFiles, seg.Name)
	}

	return res, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// moveFile attempts os.Rename, and falls back to copy+delete across filesystem boundaries.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}

	// Cross-device or rename failure fallback: copy then remove
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		_ = os.Remove(dst)
		return err
	}

	if err := dstFile.Sync(); err != nil {
		dstFile.Close()
		_ = os.Remove(dst)
		return err
	}

	if err := dstFile.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}

	srcFile.Close()
	return os.Remove(src)
}
