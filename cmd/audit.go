package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"
	"github.com/x7ssss/pg-wal-drain/pkg/fs"
	"github.com/x7ssss/pg-wal-drain/pkg/ui"
)

type slotRecord struct {
	SlotName   string
	Plugin     string
	SlotType   string
	Database   string
	Active     bool
	ActivePID  int
	RestartLSN string
	LagBytes   int64
	WalStatus  string
}

// NewAuditCmd creates the audit Cobra command.
func NewAuditCmd() *cobra.Command {
	var dsn string
	var walPath string

	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Live database inspection of replication slots, lag, and disk utilization",
		Long: `Live database inspection of PostgreSQL replication slots, lag in bytes (via pg_wal_lsn_diff),
and filesystem disk utilization. Identifies abandoned or stale slots causing WAL disk exhaustion.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAudit(dsn, walPath)
		},
	}

	cmd.Flags().StringVar(&dsn, "dsn", "", "PostgreSQL connection string (e.g. postgres://user:pass@host:5432/db) (required)")
	cmd.Flags().StringVar(&walPath, "wal-path", "", "Local path to pg_wal or data directory for filesystem space check (optional)")
	_ = cmd.MarkFlagRequired("dsn")

	return cmd
}

func runAudit(dsn string, walPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Println(ui.AlertBox("ERROR", fmt.Sprintf("Failed to open connection: %v", err)))
		return err
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		fmt.Println(ui.AlertBox("FATAL", "Cannot connect to PostgreSQL cluster",
			fmt.Sprintf("Error: %v", err),
			"Note: If PostgreSQL crashed due to 0 bytes free, clients are rejected with:",
			"  FATAL: the database system is starting up",
			"Use 'pg-wal-drain offline-inspect' for direct emergency recovery on the host.",
		))
		return err
	}

	// 1. Cluster Version and Current WAL LSN
	var pgVersion string
	var currentLSN string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&pgVersion); err != nil {
		return fmt.Errorf("failed to query version: %w", err)
	}

	_ = db.QueryRowContext(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&currentLSN)
	if currentLSN == "" {
		// PostgreSQL < 10 fallback
		_ = db.QueryRowContext(ctx, "SELECT pg_current_xlog_location()::text").Scan(&currentLSN)
	}

	// 2. Query Replication Slots
	hasWalStatus := checkColumnExists(ctx, db, "pg_replication_slots", "wal_status")
	query := `
		SELECT 
			slot_name,
			COALESCE(plugin, ''),
			slot_type,
			COALESCE(database, ''),
			active,
			COALESCE(active_pid, 0),
			COALESCE(restart_lsn::text, ''),
			COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0)::bigint AS lag_bytes
		FROM pg_replication_slots
		ORDER BY lag_bytes DESC
	`
	if hasWalStatus {
		query = `
			SELECT 
				slot_name,
				COALESCE(plugin, ''),
				slot_type,
				COALESCE(database, ''),
				active,
				COALESCE(active_pid, 0),
				COALESCE(restart_lsn::text, ''),
				COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0)::bigint AS lag_bytes,
				COALESCE(wal_status, 'unknown')
			FROM pg_replication_slots
			ORDER BY lag_bytes DESC
		`
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to query pg_replication_slots: %w", err)
	}
	defer rows.Close()

	slots := make([]slotRecord, 0)
	var totalLag int64
	for rows.Next() {
		var s slotRecord
		if hasWalStatus {
			if err := rows.Scan(&s.SlotName, &s.Plugin, &s.SlotType, &s.Database, &s.Active, &s.ActivePID, &s.RestartLSN, &s.LagBytes, &s.WalStatus); err != nil {
				return fmt.Errorf("scan error: %w", err)
			}
		} else {
			if err := rows.Scan(&s.SlotName, &s.Plugin, &s.SlotType, &s.Database, &s.Active, &s.ActivePID, &s.RestartLSN, &s.LagBytes); err != nil {
				return fmt.Errorf("scan error: %w", err)
			}
			s.WalStatus = "N/A"
		}
		slots = append(slots, s)
		totalLag += s.LagBytes
	}

	// 3. Resolve disk path for filesystem space detection
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

	diskUsage, diskErr := fs.GetDiskUsage(resolvedPath)

	// 4. Render Banner
	bannerItems := [][2]string{
		{"POSTGRES VERSION", summarizeVersion(pgVersion)},
		{"CURRENT WAL LSN", currentLSN},
		{"TOTAL SLOTS", fmt.Sprintf("%d", len(slots))},
		{"TOTAL SLOT LAG", ui.FormatBytes(totalLag)},
	}

	if diskErr == nil {
		diskStatus := "NORMAL"
		if diskUsage.UsedPct >= 90.0 {
			diskStatus = "CRITICAL"
		} else if diskUsage.UsedPct >= 80.0 {
			diskStatus = "WARNING"
		}
		bannerItems = append(bannerItems,
			[2]string{"DISK MOUNT", diskUsage.Path},
			[2]string{"DISK TOTAL", fs.FormatBytes(diskUsage.TotalBytes)},
			[2]string{"DISK AVAILABLE", fs.FormatBytes(diskUsage.AvailBytes)},
			[2]string{"DISK USAGE", fmt.Sprintf("%.2f%% [%s]", diskUsage.UsedPct, diskStatus)},
		)
	}

	fmt.Println()
	fmt.Println(ui.Banner("PG-WAL-DRAIN :: LIVE AUDIT REPORT", bannerItems))
	fmt.Println()

	// 5. Render Slots Table
	table := ui.NewTable(
		"REPLICATION SLOTS & LAG METRICS",
		"SLOT NAME",
		"TYPE",
		"ACTIVE",
		"PID",
		"RESTART LSN",
		"LAG BYTES",
		"STATUS",
	)
	table.SetAlignment(
		ui.AlignLeft,
		ui.AlignLeft,
		ui.AlignCenter,
		ui.AlignRight,
		ui.AlignLeft,
		ui.AlignRight,
		ui.AlignCenter,
	)

	hasCriticalSlot := false
	hasInactiveSlot := false

	for _, s := range slots {
		activeStr := "true"
		if !s.Active {
			activeStr = "false"
			hasInactiveSlot = true
		}

		pidStr := "-"
		if s.ActivePID > 0 {
			pidStr = fmt.Sprintf("%d", s.ActivePID)
		}

		restartStr := s.RestartLSN
		if restartStr == "" {
			restartStr = "[NONE]"
		}

		statusTag := "[OK]"
		if s.LagBytes > 10*1024*1024*1024 { // > 10GB
			statusTag = "[DANGER]"
			hasCriticalSlot = true
		} else if !s.Active && s.LagBytes > 100*1024*1024 { // > 100MB inactive
			statusTag = "[STALE]"
		} else if !s.Active {
			statusTag = "[IDLE]"
		}

		table.AddRow(
			s.SlotName,
			s.SlotType,
			activeStr,
			pidStr,
			restartStr,
			ui.FormatBytes(s.LagBytes),
			statusTag,
		)
	}

	fmt.Println(table.Render())
	fmt.Println()

	// 6. WAL directory inspection if available
	if walPath != "" {
		inspectWALDirMetrics(walPath)
	}

	// 7. Diagnostics and Actionable Guidance
	if hasCriticalSlot {
		fmt.Println(ui.AlertBox("CRITICAL", "High replication slot lag detected (>10GB)",
			"PostgreSQL cannot recycle WAL segments held by these slots.",
			"If disk space approaches 100%, the engine will PANIC and refuse connections.",
			"Run 'pg-wal-drain protect --dsn \"...\" --threshold 90.0' to enable automated circuit breaking.",
		))
	} else if hasInactiveSlot {
		fmt.Println(ui.AlertBox("WARNING", "Inactive replication slots found",
			"Disconnected slots retain WAL indefinitely until dropped or reconnected.",
			"Verify subscriber status or drop stale slots with SELECT pg_drop_replication_slot('name').",
		))
	} else if len(slots) == 0 {
		fmt.Println(ui.AlertBox("INFO", "No replication slots configured on this cluster."))
	} else {
		fmt.Println(ui.AlertBox("SUCCESS", "All replication slots are active and healthy with minimal lag."))
	}

	return nil
}

func checkColumnExists(ctx context.Context, db *sql.DB, tableName, columnName string) bool {
	var exists bool
	query := `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2
		)
	`
	_ = db.QueryRowContext(ctx, query, tableName, columnName).Scan(&exists)
	return exists
}

func summarizeVersion(ver string) string {
	parts := strings.Split(ver, "\n")
	if len(parts) > 0 {
		first := parts[0]
		if len(first) > 50 {
			return first[:47] + "..."
		}
		return first
	}
	return ver
}

func inspectWALDirMetrics(path string) {
	walDir := path
	if !strings.HasSuffix(walDir, "pg_wal") {
		sub := filepath.Join(walDir, "pg_wal")
		if info, err := os.Stat(sub); err == nil && info.IsDir() {
			walDir = sub
		}
	}

	entries, err := os.ReadDir(walDir)
	if err != nil {
		return
	}

	var segCount int
	var totalSize int64
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) == 24 {
			segCount++
			if info, err := e.Info(); err == nil {
				totalSize += info.Size()
			}
		}
	}

	var doneCount, readyCount int
	statusEntries, err := os.ReadDir(filepath.Join(walDir, "archive_status"))
	if err == nil {
		for _, se := range statusEntries {
			if strings.HasSuffix(se.Name(), ".done") {
				doneCount++
			} else if strings.HasSuffix(se.Name(), ".ready") {
				readyCount++
			}
		}
	}

	items := [][2]string{
		{"WAL PATH", walDir},
		{"TOTAL SEGMENTS", fmt.Sprintf("%d", segCount)},
		{"TOTAL WAL SIZE", ui.FormatBytes(totalSize)},
		{"ARCHIVED (.done)", fmt.Sprintf("%d", doneCount)},
		{"PENDING ARCHIVE (.ready)", fmt.Sprintf("%d", readyCount)},
	}

	fmt.Println(ui.Banner("LOCAL WAL DIRECTORY INVENTORY", items))
	fmt.Println()
}
