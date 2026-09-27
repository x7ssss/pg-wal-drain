# 🐘 pg-wal-drain

`pg-wal-drain` is a zero-dependency Go CLI tool for diagnosing and rescuing PostgreSQL clusters experiencing write-ahead log (WAL) disk exhaustion and crashes caused by abandoned or bloated replication slots.

---

## 🔍 The Core Technical Problems

### ⚠️ The Catch-22
When PostgreSQL exhausts available disk space, the database engine crashes with:
```
PANIC: could not write to file "base/...": No space left on device
```
During restart, PostgreSQL attempts crash recovery, but cannot write checkpoint records or WAL buffers:
```
FATAL: the database system is starting up
```
All client connections over SQL are rejected. As a result, running `SELECT pg_drop_replication_slot('slot_name');` over SQL is impossible.

### 💥 The `rm -f pg_wal/*` Disaster
Panicked operators often attempt to delete WAL files manually. Deleting unarchived segments or segments at or after the checkpoint redo point corrupts the database cluster, causing:
```
PANIC: could not locate a valid checkpoint record
```
Once corrupted, manual recovery requires `pg_resetwal` with potential loss of data consistency and transaction history.

### 🛡️ How `pg-wal-drain` Solves It
1. 🔍 **Direct Binary Parser**: Directly deserializes `global/pg_control` without starting the PostgreSQL engine, extracting `CheckPointCopy.Redo`, `ThisTimeLineID`, and `wal_seg_size`.
2. ⚡ **Segment Cutoff Calculation**: Computes the exact earliest safe segment cutoff: `segNo = redoLSN / segSize` formatted as `%08X%08X%08X`.
3. 📦 **Safe Evacuation**: Scans `pg_wal` and `pg_wal/archive_status/*.done`. Files with a lexicographical name strictly less than the active redo cutoff that have a corresponding `.done` archive status are classified as expendable. They can be safely evacuated to another directory or disk to free emergency space, allowing PostgreSQL to start up and finish crash recovery.
4. 🛡️ **Automated Live Circuit Breaker**: Proactively monitors disk utilization via `syscall.Statfs`. When disk usage exceeds `--threshold` (default: 90%), it drops disconnected or lagging slots (`lag_bytes > 10GB`) and triggers `CHECKPOINT;` to recycle WAL before the disk reaches 100%.

---

## 🏛️ Architecture

```
pg-wal-drain/
├── cmd/
│   ├── pg-wal-drain/
│   │   └── main.go           # CLI root setup with Cobra
│   ├── audit.go              # Live database inspection of slots and lag
│   ├── circuit_breaker.go    # Automated protection dropping stale slots (>90% disk)
│   └── offline.go            # Offline recovery via global/pg_control
├── pkg/
│   ├── control/
│   │   ├── parser.go         # Direct binary parser for global/pg_control
│   │   └── parser_test.go
│   ├── wal/
│   │   ├── scanner.go        # pg_wal inspection and safe evacuation
│   │   └── scanner_test.go
│   ├── fs/
│   │   ├── statfs.go         # Common filesystem space definitions
│   │   ├── statfs_unix.go    # syscall.Statfs implementation for Unix/Linux
│   │   └── statfs_windows.go # GetDiskFreeSpaceEx implementation for Windows
│   └── ui/
│       ├── table.go          # Monospace brutalist terminal output
│       └── table_test.go
├── build.ps1                 # Cross-platform PowerShell build script
├── Makefile                  # Cross-platform Makefile
├── go.mod
└── go.sum
```

---

## 🔧 CLI Commands

### 1. 🔍 Live Audit
Inspects replication slots, replication lag, and mount disk utilization:
```bash
pg-wal-drain audit --dsn "postgres://user:pass@host:5432/db" [--wal-path /var/lib/postgresql/data/pg_wal]
```

### 2. ⚡ Live Circuit Breaker
Monitors disk utilization and drops idle/lagging slots when disk space crosses threshold:
```bash
# Check and drop stale slots if disk usage exceeds 90%
pg-wal-drain protect --dsn "postgres://user:pass@host:5432/db" --threshold 90.0

# Simulate without dropping slots or running CHECKPOINT
pg-wal-drain protect --dsn "postgres://user:pass@host:5432/db" --threshold 90.0 --dry-run
```

### 3. 🚑 Emergency Offline Rescue
Used when PostgreSQL has crashed with zero bytes free:
```bash
# Inspect crashed PGDATA without running PostgreSQL
pg-wal-drain offline-inspect --pgdata /var/lib/postgresql/data

# Evacuate expendable (archived and pre-redo) WAL segments to backup directory
pg-wal-drain offline-inspect --pgdata /var/lib/postgresql/data --evacuate-to /tmp/wal_backup
```

After space is freed, start PostgreSQL (`pg_ctl start -D ...`), drop the bloated replication slot (`SELECT pg_drop_replication_slot('...');`), and run `CHECKPOINT;`.

---

## 📦 Building

Target binary size is under 10MB across all platforms.

### 🔧 Using Go CLI:
```bash
go build -trimpath -ldflags="-s -w" -o dist/pg-wal-drain ./cmd/pg-wal-drain
```

### 🚀 Using Makefile:
```bash
# Run tests and build
make

# Cross-compile static binaries to dist/
make cross-compile
```

### 🪟 Using PowerShell:
```powershell
.\build.ps1
```

---

## 🩺 Testing

Run all unit tests:
```bash
go test -v ./...
```

---

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

Copyright (c) 2026 x7ssss
