// Package collector provides goroutine-based system metric samplers.
//
// This file specifically contains the network collector. The network collector
// is responsible for reading network upload/download counters and converting
// them into per-second rates.
//
// In simple terms:
//   - network.go reads total network bytes sent/received.
//   - network.go compares the current reading to the previous reading.
//   - network.go calculates bytes per second and packets per second.
//   - network.go stores that information in a NetworkSnapshot.
//   - manager.go receives the snapshot and stores it in SystemState.
//   - renderer.go displays the network upload/download rates.
package collector

import (
	"context"
	"log"
	"time"

	"github.com/shirou/gopsutil/v3/net"
)

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   RunNetwork
//
// Purpose:
//   Starts the network collector loop. This method reads network counters,
//   calculates network rates over time, and sends NetworkSnapshot values through
//   the output channel.
//
// Pre-conditions:
//   - ctx must be a valid context.
//   - out must be a valid NetworkSnapshot channel.
//   - interval should be greater than zero.
//   - gopsutil must be able to read network counter information from the
//     operating system.
//
// Post-conditions:
//   - Sends network snapshots through out until ctx is cancelled.
//   - Logs collector startup, read errors, and clean shutdown.
//   - Stops its ticker before returning.
//
// Parameters and information direction:
//   - ctx: input; controls when the collector should stop.
//   - out: output; receives NetworkSnapshot values.
//   - interval: input; controls how often network data is sampled.
////////////////////////////////////////////////////////////////////////////////
func RunNetwork(ctx context.Context, out chan NetworkSnapshot, interval time.Duration) {
	// Create a ticker that fires once per interval.
	// Each tick tells the network collector to calculate a new network rate.
	ticker := time.NewTicker(interval)

	// Stop the ticker when RunNetwork exits so ticker resources are cleaned up.
	defer ticker.Stop()

	log.Println("networkCollector: started")

	// Read the first set of network counters.
	//
	// Network speed is calculated using the difference between two readings, so
	// this first reading becomes the baseline for future comparisons.
	prev, err := readCounters()
	if err != nil {
		// If the initial read fails, log it and continue with zero counters.
		// This lets the collector keep running and try again on the next tick.
		log.Printf("networkCollector: initial read error: %v", err)
		prev = netCounters{}
	}

	// Record when the baseline reading happened.
	prevTime := time.Now()

	// Send an initial zero-rate snapshot so the renderer has something to show
	// before the first real delta can be calculated.
	sendOrDropNet(out, NetworkSnapshot{Timestamp: prevTime})

	for {
		select {
		case <-ctx.Done():
			// The shared context was cancelled, so this collector should stop.
			log.Println("networkCollector: context cancelled, exiting")
			return

		case <-ticker.C:
			// Read the newest cumulative network counters.
			curr, err := readCounters()
			if err != nil {
				// A single failed network read should not crash the whole program.
				// Log the issue and try again on the next tick.
				log.Printf("networkCollector: read error: %v", err)
				continue
			}

			now := time.Now()

			// Calculate how much time passed between the last successful reading
			// and this one.
			//
			// Using the real elapsed time is more accurate than assuming the
			// ticker fired at exactly the interval.
			elapsed := now.Sub(prevTime).Seconds()
			if elapsed <= 0 {
				elapsed = interval.Seconds()
			}

			// Convert cumulative counters into per-second rates.
			snap := computeSnapshot(prev, curr, elapsed, now)

			// Send the newest network snapshot to the state manager.
			sendOrDropNet(out, snap)

			// Save the current counters as the baseline for the next tick.
			prev = curr
			prevTime = now
		}
	}
}

// netCounters stores raw cumulative network counters.
//
// These values are totals reported by the operating system. They usually count
// from boot or from when the network interface started. Because they are totals,
// the collector has to subtract the previous reading from the current reading
// to calculate rates like bytes per second.
type netCounters struct {
	// bytesSent is the total number of bytes uploaded.
	bytesSent uint64

	// bytesRecv is the total number of bytes downloaded.
	bytesRecv uint64

	// packetsSent is the total number of outgoing packets.
	packetsSent uint64

	// packetsRecv is the total number of incoming packets.
	packetsRecv uint64
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   readCounters
//
// Purpose:
//   Reads cumulative network counters from all network interfaces and combines
//   them into one netCounters value. Loopback interfaces are skipped because
//   they represent traffic inside the same machine, not real network traffic.
//
// Pre-conditions:
//   - gopsutil must be able to read network I/O counters.
//   - The operating system should provide per-interface network statistics.
//
// Post-conditions:
//   - Returns combined network counters for non-loopback interfaces.
//   - Returns an error if gopsutil cannot read the network counters.
//
// Parameters and information direction:
//   - No parameters.
//   - returns: output; combined netCounters and possible error.
////////////////////////////////////////////////////////////////////////////////
func readCounters() (netCounters, error) {
	// pernic=true means "per NIC" or "per network interface."
	//
	// This returns one counter record per interface instead of one pre-combined
	// total. We want this so we can skip loopback interfaces.
	stats, err := net.IOCounters(true)
	if err != nil {
		return netCounters{}, err
	}

	var agg netCounters

	for _, s := range stats {
		// Skip loopback traffic.
		//
		// "lo" is common on Linux.
		// "lo0" is common on macOS/BSD-style systems.
		if s.Name == "lo" || s.Name == "lo0" {
			continue
		}

		// Add this interface's counters to the aggregate total.
		agg.bytesSent += s.BytesSent
		agg.bytesRecv += s.BytesRecv
		agg.packetsSent += s.PacketsSent
		agg.packetsRecv += s.PacketsRecv
	}

	return agg, nil
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   computeSnapshot
//
// Purpose:
//   Converts two cumulative network counter readings into a NetworkSnapshot.
//   It subtracts the previous counters from the current counters, divides by the
//   elapsed time, and produces per-second upload/download rates.
//
// Pre-conditions:
//   - prev should contain the previous counter reading.
//   - curr should contain the current counter reading.
//   - elapsedSec should be greater than zero.
//   - ts should be the timestamp for the current snapshot.
//
// Post-conditions:
//   - Returns a NetworkSnapshot containing byte rates, packet rates, cumulative
//     totals, and timestamp.
//   - If a counter appears to reset or wrap around, that rate is treated as 0.
//
// Parameters and information direction:
//   - prev: input; previous cumulative network counters.
//   - curr: input; current cumulative network counters.
//   - elapsedSec: input; seconds between the two readings.
//   - ts: input; timestamp for this snapshot.
//   - returns: output; completed NetworkSnapshot.
////////////////////////////////////////////////////////////////////////////////
func computeSnapshot(prev, curr netCounters, elapsedSec float64, ts time.Time) NetworkSnapshot {
	// Helper function for converting one cumulative counter into a rate.
	//
	// Example:
	//   previous bytes received = 1000
	//   current bytes received  = 3000
	//   elapsed time            = 2 seconds
	//
	//   rate = (3000 - 1000) / 2 = 1000 bytes per second
	rate := func(prev, curr uint64) uint64 {
		if curr < prev {
			// If the current value is smaller than the previous value, the
			// counter probably reset, wrapped, or the interface restarted.
			//
			// Instead of reporting a huge incorrect number, treat this tick's
			// rate as 0.
			return 0
		}

		return uint64(float64(curr-prev) / elapsedSec)
	}

	return NetworkSnapshot{
		BytesSentPerSec:   rate(prev.bytesSent, curr.bytesSent),
		BytesRecvPerSec:   rate(prev.bytesRecv, curr.bytesRecv),
		PacketsSentPerSec: rate(prev.packetsSent, curr.packetsSent),
		PacketsRecvPerSec: rate(prev.packetsRecv, curr.packetsRecv),
		TotalBytesSent:    curr.bytesSent,
		TotalBytesRecv:    curr.bytesRecv,
		Timestamp:         ts,
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   sendOrDropNet
//
// Purpose:
//   Sends the newest network snapshot to the state manager without blocking the
//   network collector. If the channel already contains an old unread snapshot,
//   this method removes the stale value and replaces it with the newest one.
//
// Pre-conditions:
//   - out must be a valid NetworkSnapshot channel.
//   - snap should contain a network snapshot to send.
//   - out is expected to be buffered, usually with capacity 1.
//
// Post-conditions:
//   - If possible, snap is sent to out.
//   - If an older network snapshot is waiting in the channel, it may be removed.
//   - The collector does not block waiting for the state manager.
//   - If the channel is still full after draining, the snapshot is dropped and
//     the problem is logged.
//
// Parameters and information direction:
//   - out: output; channel receiving network snapshots.
//   - snap: input; newest network snapshot.
////////////////////////////////////////////////////////////////////////////////
func sendOrDropNet(out chan NetworkSnapshot, snap NetworkSnapshot) {
	select {
	case out <- snap:
		// Sent successfully.
	default:
		// The channel already has an unread network snapshot.
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
			log.Println("networkCollector: dropped snapshot, channel still full")
		}
	}
}
