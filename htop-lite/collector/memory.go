package collector

import (
	"context"
	"log"
	"time"

	"github.com/shirou/gopsutil/v3/mem"
)

// RunMemory is the entry point for the memory collector goroutine.
// It samples system memory usage on every tick and sends a MemSnapshot
// on out. The channel is bidirectional so sendOrDropMem can drain stale
// values the same way the CPU collector does.
//
// Usage:
//
//	memCh := make(chan collector.MemSnapshot, 1)
//	go collector.RunMemory(ctx, memCh, time.Second)
func RunMemory(ctx context.Context, out chan MemSnapshot, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Println("memCollector: started")

	// Sample immediately so the UI has real data on first render.
	if snap, err := sampleMemory(); err != nil {
		log.Printf("memCollector: initial sample error: %v", err)
	} else {
		sendOrDropMem(out, snap)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("memCollector: context cancelled, exiting")
			return

		case <-ticker.C:
			snap, err := sampleMemory()
			if err != nil {
				// A single failed read is not fatal — log and retry next tick.
				log.Printf("memCollector: sample error: %v", err)
				continue
			}
			sendOrDropMem(out, snap)
		}
	}
}

// sampleMemory reads current RAM and swap statistics from the OS and
// returns a fully populated MemSnapshot.
//
// On Linux, gopsutil reads /proc/meminfo. On macOS it uses sysctl(3).
// On Windows it calls GlobalMemoryStatusEx. The fields we expose are a
// lowest-common-denominator subset that is available on all platforms.
func sampleMemory() (MemSnapshot, error) {
	// ── Virtual memory (RAM) ─────────────────────────────────────────────
	vm, err := mem.VirtualMemory()
	if err != nil {
		return MemSnapshot{}, err
	}

	// ── Swap memory ───────────────────────────────────────────────────────
	// Swap is optional — systems without swap return all zeros, which is
	// fine; we handle SwapTotal == 0 gracefully below.
	sw, err := mem.SwapMemory()
	if err != nil {
		// Non-fatal: log and continue with zero swap values.
		log.Printf("memCollector: swap read error (continuing): %v", err)
		sw = &mem.SwapMemoryStat{}
	}

	var swapPct float64
	if sw.Total > 0 {
		swapPct = clampF(sw.UsedPercent, 0, 100)
	}

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

// sendOrDropMem is the memory-typed equivalent of the CPU collector's
// sendOrDrop. It replaces a stale buffered snapshot with the latest one
// rather than blocking or silently discarding fresh data.
func sendOrDropMem(out chan MemSnapshot, snap MemSnapshot) {
	select {
	case out <- snap:
		// Delivered successfully.
	default:
		// Channel full — evict stale value and send fresh one.
		select {
		case <-out:
		default:
		}
		select {
		case out <- snap:
		default:
			log.Println("memCollector: dropped snapshot, channel still full")
		}
	}
}

// clampF constrains v to [lo, hi].
// Defined here (and mirrored in cpu.go) to keep each file self-contained
// and avoid a shared internal/util package for a two-line function.
func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
