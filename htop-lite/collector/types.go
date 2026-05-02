// Package models defines the shared data types used across htop-lite.
// All collector snapshot types live here so that the collector, state,
// and ui packages can import a single, stable dependency with no risk
// of import cycles.
//
// Design rules for types in this file:
//   - All fields are exported (collectors write them, renderer reads them).
//   - All types are plain structs with value semantics — no pointers,
//     no methods that mutate, no embedded sync primitives. This makes
//     them safe to copy across goroutine boundaries via channels without
//     any additional locking.
//   - Timestamps use time.Time so callers can compute deltas or format
//     them for display without caring about the underlying clock source.
package collector

import (
	"fmt"
	"os"
	"time"
)

// ── CPU ───────────────────────────────────────────────────────────────────────

// CPUSnapshot holds a single point-in-time reading of CPU utilisation.
// All percentage fields are in the range [0, 100].
type CPUSnapshot struct {
	// UsagePercent is the aggregate utilisation across all logical cores.
	UsagePercent float64

	// CoreUsage holds per-core utilisation percentages.
	// len(CoreUsage) == CoreCount.
	CoreUsage []float64

	// CoreCount is the number of logical CPU cores detected by the OS.
	CoreCount int

	// Timestamp records when this snapshot was captured.
	Timestamp time.Time
}

// ── Memory ────────────────────────────────────────────────────────────────────

// MemSnapshot holds a single point-in-time reading of system memory.
// All byte fields are in bytes; UsedPercent is in [0, 100].
type MemSnapshot struct {
	// Total is the total amount of physical RAM installed.
	Total uint64

	// Used is the amount of RAM currently in use (total − free − buffers − cache).
	Used uint64

	// Free is the amount of RAM that is completely unallocated.
	Free uint64

	// Cached is the amount of RAM used for the page cache.
	// Reported separately so the renderer can show a nuanced breakdown.
	Cached uint64

	// Buffers is the amount of RAM used for kernel I/O buffers.
	Buffers uint64

	// UsedPercent is Used / Total × 100, pre-computed by the collector.
	UsedPercent float64

	// SwapTotal is the total swap space configured.
	SwapTotal uint64

	// SwapUsed is the amount of swap currently in use.
	SwapUsed uint64

	// SwapPercent is SwapUsed / SwapTotal × 100. Zero if no swap configured.
	SwapPercent float64

	// Timestamp records when this snapshot was captured.
	Timestamp time.Time
}

// ── Network ───────────────────────────────────────────────────────────────────

// NetworkSnapshot holds aggregate network I/O rates across all interfaces.
// Rates are computed by the collector as deltas between two consecutive reads.
type NetworkSnapshot struct {
	// BytesSentPerSec is the aggregate upload rate in bytes/second.
	BytesSentPerSec uint64

	// BytesRecvPerSec is the aggregate download rate in bytes/second.
	BytesRecvPerSec uint64

	// PacketsSentPerSec is the aggregate packets-out rate.
	PacketsSentPerSec uint64

	// PacketsRecvPerSec is the aggregate packets-in rate.
	PacketsRecvPerSec uint64

	// TotalBytesSent is the cumulative bytes sent since boot (for reference).
	TotalBytesSent uint64

	// TotalBytesRecv is the cumulative bytes received since boot (for reference).
	TotalBytesRecv uint64

	// Timestamp records when this snapshot was captured.
	Timestamp time.Time
}

// ── Process ───────────────────────────────────────────────────────────────────

// ProcessInfo describes a single running process at a point in time.
// It is designed to be cheap to copy — the only heap allocation is the
// Name string, which is typically short and shared across frames.
type ProcessInfo struct {
	// PID is the OS process identifier.
	PID int32

	// PPID is the parent process identifier.
	PPID int32

	// Name is the short executable name (e.g. "chrome", "gopls").
	// Truncated to the OS limit (~15 chars on Linux from /proc/comm).
	Name string

	// Cmdline is the full command line including arguments.
	// May be empty for kernel threads.
	Cmdline string

	// Username is the name of the user that owns this process.
	Username string

	// Status is a single-letter process state: R (running), S (sleeping),
	// D (uninterruptible sleep), Z (zombie), T (stopped), I (idle).
	Status string

	// CPUPercent is the CPU utilisation of this process over the last
	// collection interval, in [0, 100 × NumCores].
	// Values above 100 are normal on multi-core systems.
	CPUPercent float64

	// MemBytes is the resident set size (RSS) in bytes.
	MemBytes uint64

	// MemPercent is MemBytes / TotalRAM × 100.
	MemPercent float64

	// NumThreads is the number of threads in this process.
	NumThreads int32

	// Priority is the OS scheduling priority (nice value, −20 to 19).
	Priority int32

	// Nice is the nice value for this process.
	Nice int32

	// OpenFiles is the number of open file descriptors.
	// May be 0 if the collector lacks permission to read /proc/<pid>/fd.
	OpenFiles int32

	// StartTime is when this process was started.
	StartTime time.Time

	// Timestamp records when this snapshot entry was captured.
	Timestamp time.Time
}

// Kill sends SIGTERM to the process. It is a convenience method so the
// state manager doesn't need to import "os" directly.
// Returns an error if the process no longer exists or the caller lacks
// permission.
func (p ProcessInfo) Kill() error {
	proc, err := os.FindProcess(int(p.PID))
	if err != nil {
		return fmt.Errorf("find process %d: %w", p.PID, err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal process %d: %w", p.PID, err)
	}
	return nil
}

// IsKernelThread returns true if the process is a kernel thread.
// Kernel threads have an empty command line on Linux.
func (p ProcessInfo) IsKernelThread() bool {
	return p.Cmdline == ""
}

// StatusLabel returns a human-readable label for the process Status field.
func (p ProcessInfo) StatusLabel() string {
	switch p.Status {
	case "R":
		return "running"
	case "S":
		return "sleeping"
	case "D":
		return "disk-wait"
	case "Z":
		return "zombie"
	case "T":
		return "stopped"
	case "I":
		return "idle"
	default:
		return p.Status
	}
}

// ── Aggregate snapshot ────────────────────────────────────────────────────────

// SystemSnapshot is a convenience type that bundles all collector outputs
// into a single struct. It is used internally by the state manager as its
// working state and is what gets broadcast to the renderer each frame.
//
// Note: this type mirrors state.SystemState but lives in models so that
// test helpers and future CLI tools can import it without pulling in the
// full state package.
type SystemSnapshot struct {
	CPU       CPUSnapshot
	Memory    MemSnapshot
	Network   NetworkSnapshot
	Processes []ProcessInfo
}
