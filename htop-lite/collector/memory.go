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
//              This file specifically contains the memory collector. The memory collector is
//              responsible for reading RAM and swap usage while the program is running.
// In simple terms:
//   - memory.go samples RAM and swap information.
//   - memory.go stores that information in a MemSnapshot.
//   - memory.go sends the snapshot through a channel.
//   - manager.go receives the snapshot and stores it in SystemState.
//   - renderer.go displays the memory bar.
//
// Language:
//   Go
//
// External / Important Packages Used:
//   - context: used to coordinate shutdown across goroutines.
//   - log: used to write debug/status information to htop-lite.log.
//   - time: used for the collector tick rate.
//   - github.com/shirou/gopsutil/v3/mem: for ease of use in gathering/parsing
//     memory data.
//
////////////////////////////////////////////////////////////////////////////////

package collector

import (
	"context"
	"log"
	"time"

	"github.com/shirou/gopsutil/v3/mem"
)

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   RunMemory
//
// Purpose:
//   Starts the memory collector loop. This method samples memory information
//   immediately once, then continues sampling once per interval. Each successful
//   memory reading is sent through the output channel as a MemSnapshot.
//
// Pre-conditions:
//   - ctx must be a valid context.
//   - out must be a valid MemSnapshot channel.
//   - interval should be greater than zero.
//   - The gopsutil memory package must be able to read memory information from
//     the operating system.
//
// Post-conditions:
//   - Sends memory snapshots through out until ctx is cancelled.
//   - Logs collector startup, sample errors, and clean shutdown.
//   - Stops its ticker before returning.
//
// Parameters and information direction:
//   - ctx: input; controls when the collector should stop.
//   - out: output; receives MemSnapshot values.
//   - interval: input; controls how often memory data is sampled.
////////////////////////////////////////////////////////////////////////////////
func RunMemory(ctx context.Context, out chan MemSnapshot, interval time.Duration) {
	// Create a ticker that fires once per interval.
	// Every tick tells the memory collector to take another reading.
	ticker := time.NewTicker(interval)

	// Stop the ticker when RunMemory exits so ticker resources are cleaned up.
	defer ticker.Stop()

	log.Println("memCollector: started")

	// Take one sample immediately so the UI does not show empty/zero memory data
	// for a full second when the program first opens.
	if snap, err := sampleMemory(); err != nil {
		log.Printf("memCollector: initial sample error: %v", err)
	} else {
		sendOrDropMem(out, snap)
	}

	for {
		select {
		case <-ctx.Done():
			// The shared context was cancelled, so this collector should stop.
			log.Println("memCollector: context cancelled, exiting")
			return

		case <-ticker.C:
			// Time for another memory reading.
			snap, err := sampleMemory()
			if err != nil {
				// A single failed sample should not crash the whole program.
				// Log the issue and try again on the next tick.
				log.Printf("memCollector: sample error: %v", err)
				continue
			}

			// Send the newest memory snapshot to the state manager.
			sendOrDropMem(out, snap)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   sampleMemory
//
// Purpose:
//   Reads the current RAM and swap statistics from the operating system and
//   returns them as a MemSnapshot.
//
// Pre-conditions:
//   - The operating system must provide memory information.
//   - gopsutil must be able to read that information.
//   - This method is expected to run inside the memory collector goroutine.
//
// Post-conditions:
//   - Returns a MemSnapshot containing RAM, swap, usage percentages, and a
//     timestamp.
//   - Returns an error if virtual memory information cannot be read.
//   - If swap information cannot be read, the method logs the problem and
//     continues with zero-value swap data.
//
// Parameters and information direction:
//   - No parameters.
//   - returns: output; MemSnapshot and possible error.
////////////////////////////////////////////////////////////////////////////////
func sampleMemory() (MemSnapshot, error) {
	// Read physical/virtual memory statistics.
	//
	// On Linux, this information usually comes from /proc/meminfo.
	// gopsutil hides the operating-system-specific details from us.
	vm, err := mem.VirtualMemory()
	if err != nil {
		return MemSnapshot{}, err
	}

	// Read swap memory statistics.
	//
	// Swap may not exist on every system. If swap reading fails, this program
	// treats it as non-fatal and continues with empty swap values.
	sw, err := mem.SwapMemory()
	if err != nil {
		log.Printf("memCollector: swap read error (continuing): %v", err)
		sw = &mem.SwapMemoryStat{}
	}

	// Avoid dividing by zero when the system has no swap configured.
	var swapPct float64
	if sw.Total > 0 {
		swapPct = clampF(sw.UsedPercent, 0, 100)
	}

	// Build one complete memory snapshot.
	return MemSnapshot{
		Total:       vm.Total,
		Used:        vm.Used,
		Free:        vm.Free,
		Cached:      vm.Cached,
		Buffers:     vm.Buffers,
		UsedPercent: clampF(vm.UsedPercent, 0, 100),
		SwapTotal:   sw.Total,
		SwapUsed:    sw.Used,
		SwapPercent: swapPct,
		Timestamp:   time.Now(),
	}, nil
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   sendOrDropMem
//
// Purpose:
//   Sends the newest memory snapshot to the state manager without blocking the
//   memory collector. If the channel already contains an old unread snapshot,
//   this method removes the stale value and replaces it with the newest one.
//
// Pre-conditions:
//   - out must be a valid MemSnapshot channel.
//   - snap should contain a memory snapshot to send.
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
//   - out: output; channel receiving memory snapshots.
//   - snap: input; newest memory snapshot.
////////////////////////////////////////////////////////////////////////////////
func sendOrDropMem(out chan MemSnapshot, snap MemSnapshot) {
	select {
	case out <- snap:
		// Sent successfully.
	default:
		// The channel already has an unread memory snapshot.
		// Remove that stale value first.
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
			log.Println("memCollector: dropped snapshot, channel still full")
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   clampF
//
// Purpose:
//   Restricts a float64 value so it stays inside a given range. The memory
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
func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
