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
//              This file specifically contains the CPU collector. The CPU collector is
//              responsible for reading total CPU usage and per-core CPU usage while the
//              program is running.
// In simple terms:
//   - cpu.go samples CPU usage.
//   - cpu.go stores that information in a CPUSnapshot.
//   - cpu.go sends the snapshot through a channel.
//   - manager.go receives the snapshot and stores it in SystemState.
//   - renderer.go displays the CPU bars.
//
// Language:
//   Go
//
// External / Important Packages Used:
//   - context: used to coordinate shutdown across goroutines.
//   - log: used to write debug/status information to htop-lite.log.
//   - os: used to open the log file and interact with the operating system.
//   - os/signal: used to detect Ctrl+C and other interrupt signals.
//   - sync: used for WaitGroup so main waits for goroutines to finish.
//   - syscall: used for SIGINT and SIGTERM signal constants.
//   - time: used for the collector tick rate.
//
// Deficiencies:
//   - Program is designed mainly for terminal environments.
//   - Some process data may be unavailable depending on user permissions.
//   - Killing processes may fail if the user does not have permission.
//   - Terminal display may be limited on very small windows.
////////////////////////////////////////////////////////////////////////////////
package collector

import (
	"context"
	"log"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
)

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   RunCPU
//
// Purpose:
//   Starts the CPU collector loop. This method samples CPU usage immediately
//   once, then continues sampling once per interval. Each successful CPU reading
//   is sent through the output channel as a CPUSnapshot.
//
// Pre-conditions:
//   - ctx must be a valid context.
//   - out must be a valid CPUSnapshot channel.
//   - interval should be greater than zero.
//   - The gopsutil CPU package must be able to read CPU information from the
//     operating system.
//
// Post-conditions:
//   - Sends CPU snapshots through out until ctx is cancelled.
//   - Logs collector startup, errors, and clean shutdown.
//   - Stops its ticker before returning.
//
// Parameters and information direction:
//   - ctx: input; controls when the collector should stop.
//   - out: output; receives CPUSnapshot values.
//   - interval: input; controls how often CPU data is sampled.
////////////////////////////////////////////////////////////////////////////////
func RunCPU(ctx context.Context, out chan CPUSnapshot, interval time.Duration) {
	// Create a ticker that fires repeatedly at the requested interval.
	// Each tick tells the collector it is time to collect a new CPU sample.
	ticker := time.NewTicker(interval)

	// Always stop the ticker when the collector exits.
	// This prevents ticker resources from leaking.
	defer ticker.Stop()

	log.Println("cpuCollector: started")

	// Take one sample immediately.
	//
	// Without this, the UI would wait a full interval before showing CPU data.
	// Sampling right away makes the program feel responsive when it first opens.
	if snap, err := sample(); err != nil {
		log.Printf("cpuCollector: initial sample error: %v", err)
	} else {
		sendOrDrop(out, snap)
	}

	for {
		select {
		case <-ctx.Done():
			// The shared context was cancelled, so the collector should stop.
			log.Println("cpuCollector: context cancelled, exiting")
			return

		case <-ticker.C:
			// Time for another CPU reading.
			snap, err := sample()
			if err != nil {
				// One failed sample should not crash the whole program.
				// Log the problem and try again on the next tick.
				log.Printf("cpuCollector: sample error: %v", err)
				continue
			}

			// Send the newest CPU snapshot to the state manager.
			sendOrDrop(out, snap)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   sample
//
// Purpose:
//   Reads the current CPU usage from the operating system and returns it as a
//   CPUSnapshot. This includes both total CPU usage and per-core CPU usage.
//
// Pre-conditions:
//   - The operating system must provide CPU usage information.
//   - gopsutil must be able to read that information.
//   - This method is expected to run inside the CPU collector goroutine.
//
// Post-conditions:
//   - Returns a CPUSnapshot containing total usage, per-core usage, core count,
//     and timestamp.
//   - Returns an error if gopsutil fails to read CPU data.
//   - Clamps usage percentages into reasonable ranges.
//
// Parameters and information direction:
//   - No parameters.
//   - returns: output; CPUSnapshot and possible error.
////////////////////////////////////////////////////////////////////////////////
func sample() (CPUSnapshot, error) {
	// gopsutil calculates CPU usage by comparing CPU counters over a short time.
	// This interval tells gopsutil how long to wait while measuring.
	const measureInterval = 100 * time.Millisecond

	// Read overall CPU utilization across all cores.
	//
	// percpu=false means the result is one total usage value instead of one
	// value per core.
	totals, err := cpu.Percent(measureInterval, false)
	if err != nil {
		return CPUSnapshot{}, err
	}

	// Read per-core CPU utilization.
	//
	// percpu=true means the result contains one value per logical CPU core.
	//
	// Passing 0 as the interval lets gopsutil use recently collected/cached
	// counter information instead of waiting another 100ms.
	cores, err := cpu.Percent(0, true)
	if err != nil {
		return CPUSnapshot{}, err
	}

	// Ask the operating system how many logical CPU cores exist.
	//
	// true means logical cores are counted, including hyperthreaded cores.
	coreCount, err := cpu.Counts(true)
	if err != nil {
		// This is not fatal because the usage values may still be valid.
		// If core count cannot be read, fall back to the number of per-core
		// usage values returned above.
		log.Printf("cpuCollector: could not get core count: %v", err)
		coreCount = len(cores)
	}

	// Extract the total usage value.
	// gopsutil returns a slice even when percpu=false, so we check that the
	// slice actually contains a value before using it.
	var overall float64
	if len(totals) > 0 {
		overall = clamp(totals[0], 0, 100)
	}

	// Clamp each per-core usage value between 0 and 100.
	// This protects the renderer from strange values returned by the OS.
	clamped := make([]float64, len(cores))
	for i, c := range cores {
		clamped[i] = clamp(c, 0, 100)
	}

	// Return one complete CPU snapshot.
	return CPUSnapshot{
		UsagePercent: overall,
		CoreUsage:    clamped,
		CoreCount:    coreCount,
		Timestamp:    time.Now(),
	}, nil
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   sendOrDrop
//
// Purpose:
//   Sends the newest CPU snapshot to the state manager without blocking the CPU
//   collector. If the channel already contains an old unread snapshot, this
//   method removes the stale value and replaces it with the newest snapshot.
//
// Pre-conditions:
//   - out must be a valid CPUSnapshot channel.
//   - snap should contain a CPU snapshot to send.
//   - out is expected to be buffered, usually with capacity 1.
//
// Post-conditions:
//   - If possible, snap is sent to out.
//   - If an older value is waiting in the channel, it may be removed.
//   - The collector does not block waiting for the state manager.
//   - If the channel is still full after draining, the snapshot is dropped and
//     the problem is logged.
//
// Parameters and information direction:
//   - out: output; channel receiving CPU snapshots.
//   - snap: input; newest CPU snapshot.
////////////////////////////////////////////////////////////////////////////////
func sendOrDrop(out chan CPUSnapshot, snap CPUSnapshot) {
	select {
	case out <- snap:
		// Sent successfully.
	default:
		// The channel already has an unread snapshot.
		// Remove the stale value first.
		select {
		case <-out:
		default:
		}

		// Try to send the newest snapshot after draining the old one.
		select {
		case out <- snap:
		default:
			// If the channel is still full, do not block the collector.
			// Drop this snapshot and try again on the next tick.
			log.Println("cpuCollector: dropped snapshot, channel still full")
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   clamp
//
// Purpose:
//   Restricts a float64 value so it stays inside a given range. The CPU
//   collector uses this to keep percentages inside expected bounds.
//
// Pre-conditions:
//   - lo should be less than or equal to hi.
//
// Post-conditions:
//   - Returns lo if v is below lo.
//   - Returns hi if v is above hi.
//   - Returns v if v is already inside the range.
//
// Parameters and information direction:
//   - v: input; value to clamp.
//   - lo: input; lower bound.
//   - hi: input; upper bound.
//   - returns: output; clamped value.
////////////////////////////////////////////////////////////////////////////////
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
