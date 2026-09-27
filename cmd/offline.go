package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/x7ssss/pg-wal-drain/pkg/control"
	"github.com/x7ssss/pg-wal-drain/pkg/fs"
	"github.com/x7ssss/pg-wal-drain/pkg/ui"
	"github.com/x7ssss/pg-wal-drain/pkg/wal"
)

// NewOfflineInspectCmd creates the offline-inspect Cobra command.
func NewOfflineInspectCmd() *cobra.Command {
	var pgdata string
	var evacuateTo string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "offline-inspect",
		Short: "Emergency offline mode when database crashed and cannot start due to 0 bytes free",
		Long: `Emergency offline mode when PostgreSQL has crashed with:
  PANIC: could not write to file ... No space left on device
and client connections are rejected with:
  FATAL: the database system is starting up

Directly parses global/pg_control to determine the active redo LSN and timeline,
scans pg_wal to identify older segments that are already archived (.done),
and evacuates them to an external location to free emergency disk space.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOfflineInspect(pgdata, evacuateTo, dryRun)
		},
	}

	cmd.Flags().StringVar(&pgdata, "pgdata", "", "PostgreSQL cluster data directory (PGDATA) (required)")
	cmd.Flags().StringVar(&evacuateTo, "evacuate-to", "", "Target backup directory to evacuate safe WAL segments to")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview evacuation without moving files")
	_ = cmd.MarkFlagRequired("pgdata")

	return cmd
}

func runOfflineInspect(pgdata string, evacuateTo string, dryRun bool) error {
	absPGData, err := filepath.Abs(pgdata)
	if err != nil {
		return fmt.Errorf("invalid PGDATA path: %w", err)
	}

	controlFilePath := filepath.Join(absPGData, "global", "pg_control")
	if _, err := os.Stat(controlFilePath); err != nil {
		return fmt.Errorf("cannot locate %s: verify that --pgdata points to a valid PostgreSQL data directory", controlFilePath)
	}

	walDir := filepath.Join(absPGData, "pg_wal")
	if _, err := os.Stat(walDir); err != nil {
		// Fallback for PostgreSQL < 10
		alt := filepath.Join(absPGData, "pg_xlog")
		if _, altErr := os.Stat(alt); altErr == nil {
			walDir = alt
		} else {
			return fmt.Errorf("cannot locate WAL directory at %s: %w", walDir, err)
		}
	}

	// 1. Direct binary parsing of global/pg_control
	ctrl, err := control.ParseControlFile(controlFilePath)
	if err != nil {
		return fmt.Errorf("failed to parse global/pg_control: %w", err)
	}

	// 2. Scan WAL directory
	scanReport, err := wal.ScanWALDirectory(walDir, ctrl.EarliestSafeSeg)
	if err != nil {
		return fmt.Errorf("failed to scan WAL directory %s: %w", walDir, err)
	}

	// 3. Inspect filesystem disk usage
	diskUsage, diskErr := fs.GetDiskUsage(absPGData)

	// 4. Render Banner
	bannerItems := [][2]string{
		{"PGDATA DIRECTORY", absPGData},
		{"CLUSTER STATE", ctrl.State.String()},
		{"CONTROL VERSION", fmt.Sprintf("%d", ctrl.PGControlVersion)},
		{"TIMELINE ID", fmt.Sprintf("%d", ctrl.CheckPointCopy.ThisTimeLineID)},
		{"CHECKPOINT REDO LSN", fmt.Sprintf("%s (0x%016X)", control.FormatLSN(ctrl.CheckPointCopy.Redo), ctrl.CheckPointCopy.Redo)},
		{"ACTIVE REDO CUTOFF", ctrl.EarliestSafeSeg},
		{"WAL SEGMENT SIZE", fs.FormatBytes(ctrl.WALSegSize)},
	}

	if diskErr == nil {
		diskStatus := "NORMAL"
		if diskUsage.UsedPct >= 95.0 {
			diskStatus = "EXHAUSTED"
		} else if diskUsage.UsedPct >= 90.0 {
			diskStatus = "CRITICAL"
		}
		bannerItems = append(bannerItems,
			[2]string{"DISK MOUNT", diskUsage.Path},
			[2]string{"DISK AVAILABLE", fs.FormatBytes(diskUsage.AvailBytes)},
			[2]string{"DISK USAGE", fmt.Sprintf("%.2f%% [%s]", diskUsage.UsedPct, diskStatus)},
		)
	}

	fmt.Println()
	fmt.Println(ui.Banner("PG-WAL-DRAIN :: OFFLINE RECOVERY INSPECTION", bannerItems))
	fmt.Println()

	// 5. Render Breakdown Table
	table := ui.NewTable(
		"WAL SEGMENTS CLASSIFICATION & EVACUATION SAFETY",
		"CLASSIFICATION",
		"COUNT",
		"TOTAL SIZE",
		"ACTION / STATUS",
	)
	table.SetAlignment(ui.AlignLeft, ui.AlignRight, ui.AlignRight, ui.AlignLeft)

	table.AddRow(
		"Safe to Evacuate",
		fmt.Sprintf("%d", len(scanReport.SafeSegments)),
		ui.FormatBytes(scanReport.SafeBytes),
		"[EXPENDABLE] Already archived (.done) & older than Redo LSN",
	)
	table.AddRow(
		"Unarchived Older",
		fmt.Sprintf("%d", len(scanReport.UnarchivedOlder)),
		ui.FormatBytes(scanReport.UnarchivedBytes),
		"[PENDING] Older than Redo, but not yet archived (.ready/none)",
	)
	table.AddRow(
		"Active / Future Redo",
		fmt.Sprintf("%d", len(scanReport.ActiveSegments)),
		ui.FormatBytes(scanReport.ActiveBytes),
		"[CRITICAL] Required for crash recovery! DO NOT REMOVE",
	)

	fmt.Println(table.Render())
	fmt.Println()

	// Show sample of safe segments
	if len(scanReport.SafeSegments) > 0 {
		fmt.Printf("Safe segments eligible for emergency evacuation (%d files):\n", len(scanReport.SafeSegments))
		limit := 10
		if len(scanReport.SafeSegments) < limit {
			limit = len(scanReport.SafeSegments)
		}
		for i := 0; i < limit; i++ {
			fmt.Printf("  - %s (%s, archived .done)\n", scanReport.SafeSegments[i].Name, ui.FormatBytes(scanReport.SafeSegments[i].Size))
		}
		if len(scanReport.SafeSegments) > limit {
			fmt.Printf("  ... and %d more segments\n", len(scanReport.SafeSegments)-limit)
		}
		fmt.Println()
	}

	// 6. Evacuation logic
	if evacuateTo == "" {
		if len(scanReport.SafeSegments) > 0 {
			fmt.Println(ui.AlertBox("ACTION REQUIRED",
				fmt.Sprintf("%d safe segments (%s) can be evacuated to free disk space", len(scanReport.SafeSegments), ui.FormatBytes(scanReport.SafeBytes)),
				"Specify --evacuate-to to execute safe evacuation:",
				fmt.Sprintf("  pg-wal-drain offline-inspect --pgdata \"%s\" --evacuate-to /tmp/wal_backup", absPGData),
			))
		} else {
			fmt.Println(ui.AlertBox("WARNING", "No safe segments available for evacuation",
				"All WAL segments are either actively needed for crash recovery or awaiting archiving.",
				"Check if older segments can be manually archived or if disk can be expanded.",
			))
		}
		return nil
	}

	// Perform Evacuation
	evacOpts := wal.EvacuateOptions{
		TargetDir: evacuateTo,
		DryRun:    dryRun,
	}

	result, err := wal.EvacuateSafeSegments(scanReport, evacOpts)
	if err != nil {
		return fmt.Errorf("evacuation failed: %w", err)
	}

	if dryRun {
		fmt.Println(ui.AlertBox("DRY-RUN",
			fmt.Sprintf("Simulation complete: would evacuate %d segments (%s) to %s",
				result.EvacuatedCount, ui.FormatBytes(result.EvacuatedBytes), result.TargetDir),
			"Run without --dry-run to perform actual evacuation.",
		))
		return nil
	}

	// Success Report
	newDiskUsage, _ := fs.GetDiskUsage(absPGData)
	evacItems := [][2]string{
		{"EVACUATED SEGMENTS", fmt.Sprintf("%d", result.EvacuatedCount)},
		{"FREED DISK SPACE", ui.FormatBytes(result.EvacuatedBytes)},
		{"BACKUP TARGET", result.TargetDir},
	}
	if diskErr == nil {
		evacItems = append(evacItems,
			[2]string{"PREVIOUS DISK USAGE", fmt.Sprintf("%.2f%%", diskUsage.UsedPct)},
			[2]string{"NEW DISK USAGE", fmt.Sprintf("%.2f%%", newDiskUsage.UsedPct)},
			[2]string{"NEW FREE SPACE", fs.FormatBytes(newDiskUsage.AvailBytes)},
		)
	}

	fmt.Println(ui.Banner("EVACUATION SUCCESSFUL", evacItems))
	fmt.Println()

	fmt.Println(ui.AlertBox("NEXT STEPS", "PostgreSQL cluster can now start up and complete crash recovery",
		"1. Start PostgreSQL server:",
		fmt.Sprintf("     pg_ctl start -D \"%s\"", absPGData),
		"2. Connect via psql and identify the bloated replication slot:",
		"     SELECT slot_name, active, pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) FROM pg_replication_slots;",
		"3. Drop the offending replication slot to unblock WAL recycling:",
		"     SELECT pg_drop_replication_slot('slot_name');",
		"4. Trigger manual checkpoint to recycle WAL segments:",
		"     CHECKPOINT;",
	))

	return nil
}
