package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"
	"github.com/x7ssss/pg-wal-drain/pkg/fs"
	"github.com/x7ssss/pg-wal-drain/pkg/ui"
)

type candidateSlot struct {
	Name     string
	SlotType string
	Active   bool
	LagBytes int64
}

// NewProtectCmd creates the circuit breaker protect Cobra command.
func NewProtectCmd() *cobra.Command {
	var dsn string
	var threshold float64
	var dryRun bool
	var walPath string
	var lagThresholdBytes int64

	cmd := &cobra.Command{
		Use:   "protect",
		Short: "Automated protection that drops idle/stale slots when disk utilization crosses threshold",
		Long: `Live circuit breaker that monitors PostgreSQL mount disk utilization via syscall.Statfs.
When disk space crosses the specified threshold (default: 90%), it drops disconnected or lagging slots
(lag > 10GB) and executes CHECKPOINT to trigger immediate WAL recycling before 100% disk exhaustion.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProtect(dsn, threshold, dryRun, walPath, lagThresholdBytes)
		},
	}

	cmd.Flags().StringVar(&dsn, "dsn", "", "PostgreSQL connection string (e.g. postgres://user:pass@host:5432/db) (required)")
	cmd.Flags().Float64Var(&threshold, "threshold", 90.0, "Disk utilization percentage threshold to trigger circuit breaker")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Simulate actions without dropping slots or running CHECKPOINT")
	cmd.Flags().StringVar(&walPath, "wal-path", "", "Local path to pg_wal or data directory for filesystem space check (optional)")
	cmd.Flags().Int64Var(&lagThresholdBytes, "lag-threshold", 10*1024*1024*1024, "Minimum lag in bytes (default: 10GB) to classify slot as dangerous")
	_ = cmd.MarkFlagRequired("dsn")

	return cmd
}

func runProtect(dsn string, threshold float64, dryRun bool, walPath string, lagThresholdBytes int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("failed to open database connection: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		fmt.Println(ui.AlertBox("FATAL", "Cannot connect to PostgreSQL cluster",
			fmt.Sprintf("Error: %v", err),
			"If PostgreSQL crashed due to disk exhaustion, use 'pg-wal-drain offline-inspect'.",
		))
		return err
	}

	// 1. Determine filesystem path to inspect
	resolvedPath := walPath
	if resolvedPath == "" {
		var serverDataDir string
		if err := db.QueryRowContext(ctx, "SHOW data_directory").Scan(&serverDataDir); err == nil {
			if _, statErr := os.Stat(serverDataDir); statErr == nil {
				resolvedPath = serverDataDir
			}
		}
	}
	if resolvedPath == "" {
		resolvedPath = "."
	}

	diskUsage, err := fs.GetDiskUsage(resolvedPath)
	if err != nil {
		return fmt.Errorf("failed to check filesystem disk usage at %s: %w", resolvedPath, err)
	}

	// 2. Display Status Banner
	bannerItems := [][2]string{
		{"DISK MOUNT", diskUsage.Path},
		{"DISK TOTAL", fs.FormatBytes(diskUsage.TotalBytes)},
		{"DISK AVAILABLE", fs.FormatBytes(diskUsage.AvailBytes)},
		{"DISK USAGE", fmt.Sprintf("%.2f%%", diskUsage.UsedPct)},
		{"TRIGGER THRESHOLD", fmt.Sprintf("%.2f%%", threshold)},
		{"DRY RUN MODE", fmt.Sprintf("%v", dryRun)},
	}

	fmt.Println()
	fmt.Println(ui.Banner("CIRCUIT BREAKER :: DISK STATUS & THRESHOLD CHECK", bannerItems))
	fmt.Println()

	// 3. Check Threshold
	if diskUsage.UsedPct < threshold {
		fmt.Println(ui.AlertBox("SUCCESS",
			fmt.Sprintf("Disk utilization (%.2f%%) is below safety threshold (%.2f%%)", diskUsage.UsedPct, threshold),
			"No replication slots will be dropped. System is healthy.",
		))
		return nil
	}

	// 4. Threshold Exceeded: Trigger Circuit Breaker
	fmt.Println(ui.AlertBox("CRITICAL",
		fmt.Sprintf("CIRCUIT BREAKER TRIGGERED: Disk utilization %.2f%% exceeds threshold %.2f%%!", diskUsage.UsedPct, threshold),
		"Searching for disconnected or high-lag replication slots to drop...",
	))
	fmt.Println()

	// Query candidate slots: disconnected (active=false) OR lag > lagThresholdBytes
	query := `
		SELECT 
			slot_name,
			slot_type,
			active,
			COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0)::bigint AS lag_bytes
		FROM pg_replication_slots
		WHERE active = false OR COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0) > $1
		ORDER BY lag_bytes DESC
	`

	rows, err := db.QueryContext(ctx, query, lagThresholdBytes)
	if err != nil {
		return fmt.Errorf("failed to query candidate replication slots: %w", err)
	}
	defer rows.Close()

	candidates := make([]candidateSlot, 0)
	for rows.Next() {
		var cs candidateSlot
		if err := rows.Scan(&cs.Name, &cs.SlotType, &cs.Active, &cs.LagBytes); err != nil {
			return fmt.Errorf("failed to scan candidate slot: %w", err)
		}
		candidates = append(candidates, cs)
	}

	if len(candidates) == 0 {
		fmt.Println(ui.AlertBox("WARNING", "No candidate slots found to drop",
			fmt.Sprintf("No disconnected slots or slots exceeding %s lag were found.", ui.FormatBytes(lagThresholdBytes)),
			"Disk exhaustion may be due to large tables, temp files, or unarchived WAL segments.",
		))
		return nil
	}

	// Render table of candidates
	table := ui.NewTable(
		"CANDIDATE SLOTS TARGETED FOR CIRCUIT BREAKER REMOVAL",
		"SLOT NAME",
		"TYPE",
		"ACTIVE",
		"LAG BYTES",
		"ACTION",
	)
	table.SetAlignment(ui.AlignLeft, ui.AlignLeft, ui.AlignCenter, ui.AlignRight, ui.AlignCenter)

	for _, c := range candidates {
		actionStr := "DROP"
		if dryRun {
			actionStr = "[WOULD DROP]"
		}
		activeStr := "false"
		if c.Active {
			activeStr = "true"
		}
		table.AddRow(c.Name, c.SlotType, activeStr, ui.FormatBytes(c.LagBytes), actionStr)
	}

	fmt.Println(table.Render())
	fmt.Println()

	// Drop slots
	var droppedCount int
	for _, c := range candidates {
		if dryRun {
			fmt.Printf("[DRY-RUN] Would drop replication slot: %s (lag: %s, active: %v)\n",
				c.Name, ui.FormatBytes(c.LagBytes), c.Active)
			droppedCount++
			continue
		}

		fmt.Printf("Dropping replication slot: %s (lag: %s)... ", c.Name, ui.FormatBytes(c.LagBytes))
		_, err := db.ExecContext(ctx, "SELECT pg_drop_replication_slot($1)", c.Name)
		if err != nil {
			fmt.Printf("FAILED: %v\n", err)
		} else {
			fmt.Println("SUCCESS")
			droppedCount++
		}
	}

	if droppedCount == 0 {
		fmt.Println("No slots were dropped.")
		return nil
	}

	// Execute CHECKPOINT to trigger WAL recycling
	fmt.Println()
	if dryRun {
		fmt.Println("[DRY-RUN] Would execute CHECKPOINT; to recycle freed WAL segments.")
	} else {
		fmt.Println("Executing CHECKPOINT to trigger immediate WAL recycling...")
		startCp := time.Now()
		_, err = db.ExecContext(ctx, "CHECKPOINT;")
		if err != nil {
			fmt.Printf("Warning: CHECKPOINT failed: %v\n", err)
		} else {
			fmt.Printf("CHECKPOINT completed in %v.\n", time.Since(startCp).Round(time.Millisecond))
		}

		// Re-check disk space
		newUsage, err := fs.GetDiskUsage(resolvedPath)
		if err == nil {
			var recoveredBytes int64
			if newUsage.FreeBytes > diskUsage.FreeBytes {
				recoveredBytes = int64(newUsage.FreeBytes - diskUsage.FreeBytes)
			}
			summaryItems := [][2]string{
				{"SLOTS DROPPED", fmt.Sprintf("%d", droppedCount)},
				{"PREVIOUS DISK USAGE", fmt.Sprintf("%.2f%%", diskUsage.UsedPct)},
				{"NEW DISK USAGE", fmt.Sprintf("%.2f%%", newUsage.UsedPct)},
				{"FREED DISK SPACE", ui.FormatBytes(recoveredBytes)},
			}
			fmt.Println()
			fmt.Println(ui.Banner("RECOVERY SUMMARY", summaryItems))
			fmt.Println()
			fmt.Println(ui.AlertBox("SUCCESS", "Circuit breaker dropped slots and initiated WAL recycling.",
				"Monitor disk usage to ensure usage stabilizes below the safety threshold.",
			))
		}
	}

	return nil
}
