////////////////////////////////////////////////////////////////////////////////
// Assignment Project: Learn a New (to You!) Programming Language Part III
// Author: Andy Clements (andywclements@arizona.edu)
//         Cora Clements (coraclements@arizona.edu)
//
// Course: CSc 372
// Instructor: L. McCann
// TAs: Muaz Ali, Daniel Reynaldo
// Due Date: May 4th, 2026
//
// Description:
// Package collector defines the shared data types used by the collector,
// state, and ui packages.
//
// These structs are the "data containers" for htop-lite. The collector
// goroutines fill these structs with system information, the state manager
// combines them into a larger SystemState, and the renderer reads them to
// draw the terminal UI.
//
// Design idea:
//   - Collectors gather raw system information.
//   - The state manager organizes the newest values.
//   - The renderer displays the values.
//
// All fields are exported, meaning they start with capital letters, so other
// packages can read them.
//
// These structs are intentionally simple. They do not contain complicated
// methods or internal locks. This makes them safe and easy to copy through
// channels between goroutines.
//
// Language:
//   Go
//
// External / Important Packages Used:
//   - fmt - for formating data
//   - os: used to open the log file and interact with the operating system.
//   - time: used for the collector tick rate.
//
////////////////////////////////////////////////////////////////////////////////
package collector

import (
	"fmt"
	"os"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// CPU snapshot type
// ─────────────────────────────────────────────────────────────────────────────

// CPUSnapshot holds one reading of the computer's CPU usage.
//
// A "snapshot" means this struct represents the CPU information at one moment
// in time. The CPU collector creates new CPUSnapshot values repeatedly while
// the program runs.
type CPUSnapshot struct {
	// UsagePercent is the total CPU usage across all logical CPU cores.
	//
	// Example:
	//   If UsagePercent is 25.0, the computer is using about 25% of its total
	//   CPU capacity at the time this snapshot was collected.
	UsagePercent float64

	// CoreUsage stores the CPU usage for each individual logical core.
	//
	// Example:
	//   If the computer has 4 logical cores, this slice may contain 4 values:
	//     []float64{12.5, 40.0, 8.0, 20.3}
	//
	// Each value is a percentage from 0 to 100.
	CoreUsage []float64

	// CoreCount is the number of logical CPU cores detected by the operating
	// system.
	//
	// Logical cores include hyperthreaded cores, so this may be higher than the
	// number of physical CPU cores.
	CoreCount int

	// Timestamp records when this CPU snapshot was collected.
	//
	// The renderer can use this for display timing or uptime-style information.
	Timestamp time.Time
}

// ─────────────────────────────────────────────────────────────────────────────
// Memory snapshot type
// ─────────────────────────────────────────────────────────────────────────────

// MemSnapshot holds one reading of system memory and swap usage.
//
// This struct represents the computer's RAM and swap information at one moment
// in time.
type MemSnapshot struct {
	// Total is the total amount of physical RAM installed, measured in bytes.
	Total uint64

	// Used is the amount of RAM currently being used, measured in bytes.
	//
	// This is usually calculated by the operating system or gopsutil.
	Used uint64

	// Free is the amount of RAM that is completely unused, measured in bytes.
	Free uint64

	// Cached is RAM being used for cached files/data.
	//
	// Cached memory can often be reused by the operating system if another
	// program needs it, so it is not always "bad" memory usage.
	Cached uint64

	// Buffers is RAM used by the operating system for I/O buffers.
	//
	// Buffers are temporary holding areas used while reading/writing data.
	Buffers uint64

	// UsedPercent is the percentage of total RAM currently in use.
	//
	// Example:
	//   63.5 means about 63.5% of RAM is being used.
	UsedPercent float64

	// SwapTotal is the total amount of swap space configured, measured in bytes.
	//
	// Swap is disk space the operating system can use as overflow memory.
	SwapTotal uint64

	// SwapUsed is the amount of swap currently being used, measured in bytes.
	SwapUsed uint64

	// SwapPercent is the percentage of swap space currently being used.
	//
	// If the system has no swap, this should stay at 0.
	SwapPercent float64

	// Timestamp records when this memory snapshot was collected.
	Timestamp time.Time
}

// ─────────────────────────────────────────────────────────────────────────────
// Network snapshot type
// ─────────────────────────────────────────────────────────────────────────────

// NetworkSnapshot holds one reading of network activity.
//
// Some fields store rates, like bytes per second. Other fields store total
// cumulative values since boot. The network collector calculates rates by
// comparing the current network counters to the previous network counters.
type NetworkSnapshot struct {
	// BytesSentPerSec is the upload speed in bytes per second.
	BytesSentPerSec uint64

	// BytesRecvPerSec is the download speed in bytes per second.
	BytesRecvPerSec uint64

	// PacketsSentPerSec is the number of outgoing packets per second.
	PacketsSentPerSec uint64

	// PacketsRecvPerSec is the number of incoming packets per second.
	PacketsRecvPerSec uint64

	// TotalBytesSent is the total number of bytes sent since the system or
	// network counter started tracking.
	TotalBytesSent uint64

	// TotalBytesRecv is the total number of bytes received since the system or
	// network counter started tracking.
	TotalBytesRecv uint64

	// Timestamp records when this network snapshot was collected.
	Timestamp time.Time
}

// ─────────────────────────────────────────────────────────────────────────────
// Process information type
// ─────────────────────────────────────────────────────────────────────────────

// ProcessInfo describes one running process.
//
// A process is a running program on the computer. The process collector creates
// a ProcessInfo value for each process it can inspect.
type ProcessInfo struct {
	// PID is the process ID.
	//
	// The operating system uses this number to identify a running process.
	PID int32

	// PPID is the parent process ID.
	//
	// This is the PID of the process that started this process.
	PPID int32

	// Name is the short executable/process name.
	//
	// Example:
	//   "chrome", "bash", "go", "code"
	Name string

	// Cmdline is the full command line used to start the process.
	//
	// This may include command-line arguments. It may be empty for some system
	// or kernel processes.
	Cmdline string

	// Username is the user account that owns the process.
	Username string

	// Status is the operating system's short process-state code.
	//
	// Common examples:
	//   R = running
	//   S = sleeping
	//   D = waiting in uninterruptible sleep
	//   Z = zombie process
	//   T = stopped
	//   I = idle
	Status string

	// CPUPercent is how much CPU this process is using.
	//
	// On multi-core systems, a process can sometimes show above 100% because
	// it may be using more than one logical core.
	CPUPercent float64

	// MemBytes is the amount of physical memory/RAM used by this process,
	// measured in bytes.
	MemBytes uint64

	// MemPercent is the percentage of total system memory used by this process.
	MemPercent float64

	// NumThreads is the number of threads currently used by this process.
	//
	// A thread is a smaller path of execution inside a process.
	NumThreads int32

	// Priority is the operating system scheduling priority for this process.
	//
	// The scheduler uses this to help decide how much CPU time the process gets.
	Priority int32

	// Nice is the process nice value.
	//
	// On Unix/Linux systems, nice values usually range from -20 to 19.
	// Lower numbers mean higher priority, while higher numbers mean the process
	// is being "nicer" to other processes.
	Nice int32

	// OpenFiles is the number of open file descriptors for this process.
	//
	// This may be 0 if htop-lite does not have permission to read that
	// information.
	OpenFiles int32

	// StartTime records when the process started.
	StartTime time.Time

	// Timestamp records when this process information was collected.
	Timestamp time.Time
}

// ─────────────────────────────────────────────────────────────────────────────
// Process helper method
// ─────────────────────────────────────────────────────────────────────────────

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   Kill
//
// Purpose:
//   Attempts to send an interrupt signal to this process. This is used when
//   the user selects a process in the UI and presses the kill key.
//
// Pre-conditions:
//   - p.PID should refer to a real process.
//   - The process may still be running, but it could also have exited already.
//   - The user must have permission to signal the process.
//
// Post-conditions:
//   - If the process is found and the signal succeeds, the process receives an
//     interrupt signal.
//   - If the process cannot be found or cannot be signaled, an error is returned.
//   - This method does not directly remove the process from the UI. The process
//     collector will update the process list on the next refresh.
//
// Parameters and information direction:
//   - p: input; the ProcessInfo value whose PID should be signaled.
//   - returns: output; nil on success, or an error explaining what failed.
////////////////////////////////////////////////////////////////////////////////
func (p ProcessInfo) Kill() error {
	// Ask the operating system to find the process with this PID.
	proc, err := os.FindProcess(int(p.PID))
	if err != nil {
		return fmt.Errorf("find process %d: %w", p.PID, err)
	}

	// Send an interrupt signal to the process.
	//
	// On Unix-like systems, this is similar to asking the process to stop.
	// It is not guaranteed to work because the process may ignore the signal,
	// may have already exited, or may belong to another user.
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal process %d: %w", p.PID, err)
	}

	return nil
}
