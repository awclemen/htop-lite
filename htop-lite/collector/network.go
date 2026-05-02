package collector

import (
	"context"
	"log"
	"time"

	"github.com/shirou/gopsutil/v3/net"
)

// RunNetwork is the entry point for the network collector goroutine.
// It computes per-second byte and packet rates by diffing consecutive
// cumulative counter reads from the OS, then sends a NetworkSnapshot on out.
//
// The first tick always produces zero rates because there is no previous
// reading to diff against — this is expected and identical to how the
// CPU collector behaves on its first sample.
//
// Usage:
//
//	netCh := make(chan collector.NetworkSnapshot, 1)
//	go collector.RunNetwork(ctx, netCh, time.Second)
func RunNetwork(ctx context.Context, out chan NetworkSnapshot, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Println("networkCollector: started")

	// Seed the previous-counters baseline so the first real tick can
	// compute a meaningful delta. We send a zero snapshot immediately
	// so the UI has something to display on first render.
	prev, err := readCounters()
	if err != nil {
		log.Printf("networkCollector: initial read error: %v", err)
		prev = netCounters{}
	}
	prevTime := time.Now()
	sendOrDropNet(out, NetworkSnapshot{Timestamp: prevTime})

	for {
		select {
		case <-ctx.Done():
			log.Println("networkCollector: context cancelled, exiting")
			return

		case <-ticker.C:
			curr, err := readCounters()
			if err != nil {
				log.Printf("networkCollector: read error: %v", err)
				continue
			}
			now := time.Now()

			// Elapsed seconds since the last successful read. Using the
			// real elapsed time rather than assuming exactly `interval`
			// seconds gives accurate rates even when the ticker fires late
			// (e.g. under system load).
			elapsed := now.Sub(prevTime).Seconds()
			if elapsed <= 0 {
				elapsed = interval.Seconds()
			}

			snap := computeSnapshot(prev, curr, elapsed, now)
			sendOrDropNet(out, snap)

			prev = curr
			prevTime = now
		}
	}
}

// netCounters holds the raw cumulative OS network counters for all
// interfaces combined. These are monotonically increasing since boot;
// we diff consecutive readings to get per-second rates.
type netCounters struct {
	bytesSent   uint64
	bytesRecv   uint64
	packetsSent uint64
	packetsRecv uint64
}

// readCounters aggregates the per-interface counters from the OS into a
// single netCounters value. Loopback (lo) is excluded because it inflates
// apparent traffic and is not useful for monitoring real network activity.
func readCounters() (netCounters, error) {
	// pernic=true returns one entry per interface so we can filter loopback.
	stats, err := net.IOCounters(true)
	if err != nil {
		return netCounters{}, err
	}

	var agg netCounters
	for _, s := range stats {
		if s.Name == "lo" || s.Name == "lo0" {
			continue // skip loopback
		}
		agg.bytesSent += s.BytesSent
		agg.bytesRecv += s.BytesRecv
		agg.packetsSent += s.PacketsSent
		agg.packetsRecv += s.PacketsRecv
	}
	return agg, nil
}

// computeSnapshot derives a NetworkSnapshot from two counter readings and
// the elapsed time between them. It guards against counter resets (which
// can happen after an interface flap or OS counter overflow) by treating
// any negative delta as zero.
func computeSnapshot(prev, curr netCounters, elapsedSec float64, ts time.Time) NetworkSnapshot {
	rate := func(prev, curr uint64) uint64 {
		if curr < prev {
			// Counter wrapped or interface was reset — treat as zero for
			// this tick rather than reporting a huge negative spike.
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

// sendOrDropNet is the network-typed equivalent of sendOrDrop.
func sendOrDropNet(out chan NetworkSnapshot, snap NetworkSnapshot) {
	select {
	case out <- snap:
	default:
		select {
		case <-out:
		default:
		}
		select {
		case out <- snap:
		default:
			log.Println("networkCollector: dropped snapshot, channel still full")
		}
	}
}
