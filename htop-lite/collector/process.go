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
// Description: Package collector provides goroutine-based system metric samplers.
//              This file specifically contains the process collector. The process collector
//              is responsible for reading the operating system's current process table and
//              turning each readable process into a ProcessInfo value.
//
// In simple terms:
//   - process.go asks the OS for the current list of processes.
//   - process.go reads details about each process.
//   - process.go stores those details in ProcessInfo structs.
//   - process.go sends the full process list through a channel.
//   - manager.go sorts/filters that list.
//   - renderer.go displays the process table.
//
// Language:
//   Go
//
// External / Important Packages Used:
//   - context: used to coordinate shutdown across goroutines.
//   - log: used to write debug/status information to htop-lite.log.
//   - time: used for the collector tick rate.
//   - github.com/shirou/gopsutil/v3/process - package used for gathering
//     and filtering process data.
//
////////////////////////////////////////////////////////////////////////////////
package collector

import (
	"context"
	"log"
	"time"

	gopsProc "github.com/shirou/gopsutil/v3/process"
)

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   RunProcesses
//
// Purpose:
//   Starts the process collector loop. This method samples the current running
//   processes immediately once, then continues sampling once per interval. Each
//   successful process list is sent through the output channel.
//
// Pre-conditions:
//   - ctx must be a valid context.
//   - out must be a valid channel for []ProcessInfo.
//   - interval should be greater than zero.
//   - gopsutil must be able to read process information from the operating
//     system.
//
// Post-conditions:
//   - Sends process-list snapshots through out until ctx is cancelled.
//   - Logs collector startup, sample errors, and clean shutdown.
//   - Stops its ticker before returning.
//
// Parameters and information direction:
//   - ctx: input; controls when the collector should stop.
//   - out: output; receives slices of ProcessInfo.
//   - interval: input; controls how often process data is sampled.
////////////////////////////////////////////////////////////////////////////////
func RunProcesses(ctx context.Context, out chan []ProcessInfo, interval time.Duration) {
	// Create a ticker that fires once per interval.
	// Every tick tells the process collector to refresh the process list.
	ticker := time.NewTicker(interval)

	// Stop the ticker when this method exits so ticker resources are cleaned up.
	defer ticker.Stop()

	log.Println("processCollector: started")

	// Take one process sample immediately.
	//
	// Without this, the UI would show an empty process list until the first
	// ticker event happens.
	if procs, err := sampleProcesses(); err != nil {
		log.Printf("processCollector: initial sample error: %v", err)
	} else {
		sendOrDropProc(out, procs)
	}

	for {
		select {
		case <-ctx.Done():
			// The shared context was cancelled, so this collector should stop.
			log.Println("processCollector: context cancelled, exiting")
			return

		case <-ticker.C:
			// Time for another process-table reading.
			procs, err := sampleProcesses()
			if err != nil {
				// A single failed sample should not crash the whole program.
				// Log the issue and try again on the next tick.
				log.Printf("processCollector: sample error: %v", err)
				continue
			}

			// Send the newest process list to the state manager.
			sendOrDropProc(out, procs)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   sampleProcesses
//
// Purpose:
//   Reads the current process table from the operating system and returns a
//   slice of ProcessInfo values. Each ProcessInfo represents one readable
//   running process.
//
// Pre-conditions:
//   - gopsutil must be able to request the current process list.
//   - Some processes may disappear while this method is running.
//   - Some processes may not be readable due to permissions.
//
// Post-conditions:
//   - Returns a slice containing readable process information.
//   - Skips processes that exit during collection.
//   - Skips processes whose required fields cannot be read.
//   - Returns an error only if the initial process list cannot be fetched.
//
// Parameters and information direction:
//   - No parameters.
//   - returns: output; slice of ProcessInfo and possible error.
////////////////////////////////////////////////////////////////////////////////
func sampleProcesses() ([]ProcessInfo, error) {
	// Fetch all currently known processes from the OS.
	//
	// On Linux, this usually reads from /proc.
	procs, err := gopsProc.Processes()
	if err != nil {
		return nil, err
	}

	// Preallocate the result slice with enough room for the process list.
	//
	// The length starts at 0 because some processes may be skipped, but the
	// capacity starts at len(procs) to avoid repeated memory allocations.
	result := make([]ProcessInfo, 0, len(procs))

	for _, p := range procs {
		// Build a ProcessInfo struct for one process.
		info, err := buildProcessInfo(p)
		if err != nil {
			// Processes can exit between listing and inspection.
			// Permissions can also prevent reading some processes.
			// This is normal, so skip that process and continue.
			continue
		}

		result = append(result, info)
	}

	return result, nil
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   buildProcessInfo
//
// Purpose:
//   Reads details for one process and returns them as a ProcessInfo struct.
//   This method treats the process name as required, but most other fields are
//   optional. If optional fields cannot be read, they receive safe default
//   values and collection continues.
//
// Pre-conditions:
//   - p should point to a process returned by gopsutil.
//   - The process may still exist, or it may have exited already.
//   - Some process fields may be unavailable due to permissions.
//
// Post-conditions:
//   - Returns a populated ProcessInfo struct if required data can be read.
//   - Returns an error if the process name cannot be read.
//   - Uses default values for non-critical fields that cannot be read.
//
// Parameters and information direction:
//   - p: input; gopsutil process pointer.
//   - returns: output; ProcessInfo and possible error.
////////////////////////////////////////////////////////////////////////////////
func buildProcessInfo(p *gopsProc.Process) (ProcessInfo, error) {
	// PID is always available because gopsutil used it to create the process
	// object.
	pid := p.Pid

	// Name is the short executable name.
	//
	// This is required because the UI needs something meaningful to display in
	// the NAME column. If the name cannot be read, skip this process.
	name, err := p.Name()
	if err != nil {
		return ProcessInfo{}, err
	}

	// PPID is the parent process ID.
	//
	// This is useful information, but not critical for the UI. If it cannot be
	// read, use 0 as a safe default.
	ppid, err := p.Ppid()
	if err != nil {
		ppid = 0
	}

	// Cmdline is the full command line used to start the process.
	//
	// Some processes, especially kernel threads, may have an empty command line.
	cmdlineSlice, err := p.CmdlineSlice()
	var cmdline string
	if err == nil && len(cmdlineSlice) > 0 {
		cmdline = joinCmdline(cmdlineSlice)
	}

	// Status is the process state, such as R, S, D, Z, T, or I.
	statuses, err := p.Status()
	var status string
	if err == nil && len(statuses) > 0 {
		status = statuses[0]
	}

	// CPUPercent returns the process CPU usage.
	//
	// gopsutil calculates this as a delta over time, so the first reading for a
	// new process may be 0. Later readings become more meaningful.
	cpuPct, err := p.CPUPercent()
	if err != nil {
		cpuPct = 0
	}

	// MemoryInfo gives memory details such as RSS.
	//
	// RSS means resident set size, which is the amount of physical RAM currently
	// used by the process.
	var memBytes uint64
	var memPct float32

	if mi, err := p.MemoryInfo(); err == nil && mi != nil {
		memBytes = mi.RSS
	}

	// MemoryPercent gives this process's memory use as a percentage of total
	// system memory.
	if mp, err := p.MemoryPercent(); err == nil {
		memPct = mp
	}

	// Number of threads used by the process.
	numThreads, err := p.NumThreads()
	if err != nil {
		numThreads = 0
	}

	// Nice value controls process scheduling priority on Unix/Linux systems.
	//
	// Lower nice values usually mean higher priority.
	nice, err := p.Nice()
	if err != nil {
		nice = 0
	}

	// Username is the account that owns the process.
	//
	// This can fail in some environments, such as containers or permission-
	// restricted systems.
	username, err := p.Username()
	if err != nil {
		username = "?"
	}

	// Open file descriptor count.
	//
	// This often requires permission to inspect /proc/<pid>/fd. If the program
	// cannot read it, keep the default value of 0.
	var openFiles int32
	if fds, err := p.NumFDs(); err == nil {
		openFiles = fds
	}

	// Process creation/start time.
	var startTime time.Time
	if ms, err := p.CreateTime(); err == nil {
		startTime = time.UnixMilli(ms)
	}

	// Build and return the ProcessInfo used by the rest of the program.
	return ProcessInfo{
		PID:        pid,
		PPID:       ppid,
		Name:       name,
		Cmdline:    cmdline,
		Username:   username,
		Status:     status,
		CPUPercent: clampF(cpuPct, 0, 100*1024), // allow >100% on multi-core
		MemBytes:   memBytes,
		MemPercent: clampF(float64(memPct), 0, 100),
		NumThreads: numThreads,
		Nice:       nice,
		OpenFiles:  openFiles,
		StartTime:  startTime,
		Timestamp:  time.Now(),
	}, nil
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   joinCmdline
//
// Purpose:
//   Reassembles a command-line argument slice into one display string.
//
// Pre-conditions:
//   - args may contain zero or more command-line arguments.
//   - This string is only for display, not for re-running the command.
//
// Post-conditions:
//   - Returns an empty string if args is empty.
//   - Otherwise returns the arguments separated by spaces.
//
// Parameters and information direction:
//   - args: input; command-line argument slice.
//   - returns: output; display string.
////////////////////////////////////////////////////////////////////////////////
func joinCmdline(args []string) string {
	if len(args) == 0 {
		return ""
	}

	// Estimate the needed capacity so the byte slice does not need to grow as
	// often while building the string.
	total := 0
	for _, a := range args {
		total += len(a) + 1
	}

	buf := make([]byte, 0, total)

	for i, a := range args {
		if i > 0 {
			buf = append(buf, ' ')
		}
		buf = append(buf, a...)
	}

	return string(buf)
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   sendOrDropProc
//
// Purpose:
//   Sends the newest process list to the state manager without blocking the
//   process collector. If the channel already contains an old unread process
//   list, this method removes the stale list and replaces it with the newest
//   one.
//
// Pre-conditions:
//   - out must be a valid channel for []ProcessInfo.
//   - procs should contain the newest process-list snapshot.
//   - out is expected to be buffered, usually with capacity 1.
//
// Post-conditions:
//   - If possible, procs is sent to out.
//   - If an older process list is waiting in the channel, it may be removed.
//   - The collector does not block waiting for the state manager.
//   - If the channel is still full after draining, the process list is dropped
//     and the problem is logged.
//
// Parameters and information direction:
//   - out: output; channel receiving process-list snapshots.
//   - procs: input; newest process list.
////////////////////////////////////////////////////////////////////////////////
func sendOrDropProc(out chan []ProcessInfo, procs []ProcessInfo) {
	select {
	case out <- procs:
		// Sent successfully.
	default:
		// The channel already has an unread process list.
		// Remove that stale value first.
		select {
		case <-out:
		default:
		}

		// Try to send the newest process list after draining the old one.
		select {
		case out <- procs:
		default:
			// If the channel is still full, do not block the collector.
			// Drop this snapshot and try again on the next tick.
			log.Println("processCollector: dropped snapshot, channel still full")
		}
	}
}
