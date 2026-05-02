package collector

import (
	"context"
	"log"
	"time"

	gopsProc "github.com/shirou/gopsutil/v3/process"
)

// RunProcesses is the entry point for the process collector goroutine.
// It snapshots all running processes on every tick and sends the full
// list on out. The list is unsorted and unfiltered — the state manager
// owns sorting and filtering so the collector stays simple.
//
// Usage:
//
//	procCh := make(chan []collector.ProcessInfo, 1)
//	go collector.RunProcesses(ctx, procCh, time.Second)
func RunProcesses(ctx context.Context, out chan []ProcessInfo, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Println("processCollector: started")

	// Sample immediately so the UI has a populated process list on the
	// first render rather than showing an empty table for one full tick.
	if procs, err := sampleProcesses(); err != nil {
		log.Printf("processCollector: initial sample error: %v", err)
	} else {
		sendOrDropProc(out, procs)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("processCollector: context cancelled, exiting")
			return

		case <-ticker.C:
			procs, err := sampleProcesses()
			if err != nil {
				log.Printf("processCollector: sample error: %v", err)
				continue
			}
			sendOrDropProc(out, procs)
		}
	}
}

// sampleProcesses reads the current process table from the OS and returns
// a slice of ProcessInfo, one entry per running process.
//
// Processes that disappear between the initial listing and the per-process
// detail reads are silently skipped — this is normal and expected since
// processes can exit at any time during the scan.
//
// Processes we lack permission to inspect (e.g. some kernel threads on
// hardened systems) are also skipped rather than causing the whole
// sample to fail.
func sampleProcesses() ([]ProcessInfo, error) {
	// Fetch all PIDs from the OS. On Linux this reads /proc.
	procs, err := gopsProc.Processes()
	if err != nil {
		return nil, err
	}

	result := make([]ProcessInfo, 0, len(procs))

	for _, p := range procs {
		info, err := buildProcessInfo(p)
		if err != nil {
			// Process likely exited between listing and inspection.
			// This is normal — skip silently.
			continue
		}
		result = append(result, info)
	}

	return result, nil
}

// buildProcessInfo reads all available fields for a single process and
// returns a ProcessInfo. It tolerates partial failures — if a non-critical
// field (e.g. OpenFiles, Username) cannot be read due to permissions, the
// field is left at its zero value and collection continues.
func buildProcessInfo(p *gopsProc.Process) (ProcessInfo, error) {
	// PID is always available — it's what we used to look up the process.
	pid := p.Pid

	// Name — short executable name from /proc/<pid>/comm.
	// This is a hard requirement; if we can't get the name, skip the process.
	name, err := p.Name()
	if err != nil {
		return ProcessInfo{}, err
	}

	// PPID — parent process ID.
	ppid, err := p.Ppid()
	if err != nil {
		ppid = 0 // non-critical, default to 0
	}

	// Command line — full argv[]. May be empty for kernel threads.
	cmdlineSlice, err := p.CmdlineSlice()
	var cmdline string
	if err == nil && len(cmdlineSlice) > 0 {
		// Reconstruct as a single space-separated string for display.
		cmdline = joinCmdline(cmdlineSlice)
	}

	// Status — single-letter process state (R, S, D, Z, T, I).
	statuses, err := p.Status()
	var status string
	if err == nil && len(statuses) > 0 {
		status = statuses[0]
	}

	// CPU percent — usage over the interval since last call.
	// gopsutil tracks the delta internally per-PID across calls.
	// The first call for a new PID always returns 0; subsequent calls
	// return a meaningful delta. This is expected behaviour.
	cpuPct, err := p.CPUPercent()
	if err != nil {
		cpuPct = 0
	}

	// Memory info — RSS (resident set size) in bytes.
	var memBytes uint64
	var memPct float32
	if mi, err := p.MemoryInfo(); err == nil && mi != nil {
		memBytes = mi.RSS
	}
	if mp, err := p.MemoryPercent(); err == nil {
		memPct = mp
	}

	// Number of threads.
	numThreads, err := p.NumThreads()
	if err != nil {
		numThreads = 0
	}

	// Nice / priority values.
	nice, err := p.Nice()
	if err != nil {
		nice = 0
	}

	// Username — may fail if UID lookup fails (e.g. container environments).
	username, err := p.Username()
	if err != nil {
		username = "?"
	}

	// Open file descriptor count — requires root or process ownership on
	// most Linux systems. Fail gracefully.
	var openFiles int32
	if fds, err := p.NumFDs(); err == nil {
		openFiles = fds
	}

	// Process creation time.
	var startTime time.Time
	if ms, err := p.CreateTime(); err == nil {
		startTime = time.UnixMilli(ms)
	}

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

// joinCmdline reassembles a cmdline slice into a display string.
// Arguments containing spaces are left as-is (we don't requote them)
// since this is for display only, not re-execution.
func joinCmdline(args []string) string {
	if len(args) == 0 {
		return ""
	}
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

// sendOrDropProc is the process-list equivalent of sendOrDrop.
// Because the process list is a slice (heap-allocated), draining and
// replacing is especially important — we don't want to accumulate a
// backlog of stale large slices while the renderer is busy.
func sendOrDropProc(out chan []ProcessInfo, procs []ProcessInfo) {
	select {
	case out <- procs:
	default:
		select {
		case <-out:
		default:
		}
		select {
		case out <- procs:
		default:
			log.Println("processCollector: dropped snapshot, channel still full")
		}
	}
}
