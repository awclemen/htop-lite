package main
////////////////////////////////////////////////////////////////////////////////
// Assignment Project: Learn a New (to You!) Programming Language Part III
// Author: Andy Clements (andywclements@arizona.edu)
//         Cora Clements (coraclements@arizona.edu)
//
// Course: Csc 372
// Instructor: L. McCann
// TAs Muaz Ali, Daniel Reynaldo
// Due Date: May 4th 2026
//
// Description: minihtop - a miniture version of htop that runs in the terminal
//
// Language: Go
// Ex. Packages: context, log, os, os/signal, sync, syscall, time
//
// Deficiencies:
////////////////////////////////////////////////////////////////////////////////

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"htop-lite/collector"
	"htop-lite/events"
	"htop-lite/state"
	"htop-lite/ui"
)

// constant collectionRate of 1 second for all channels
const tickRate = time.Second

////////////////////////////////////////////////////////////////////////////////
// Method name: main
// Purpose:
// Pre- and Post- conditions:
// Parameters and information direction:
////////////////////////////////////////////////////////////////////////////////
func main() {
	// set up logging to keep message from going to terminal
	logFile, err := os.OpenFile("htop-lite.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("failed to open log file: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	log.Println("htop-lite starting...")

	// set up context
	// cancelled when user presses 'q' or 'CRTL-C'
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Listen for OS interrupt signals (Ctrl+C, SIGTERM).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received signal: %v — shutting down", sig)
		cancel()
	}()

	// set up collecters with their own channels
	// one channel per data type
	// Buffered channels of 1 - makes sure a collector never blocks on
	// waiting for the state manager and the latest data gets sent
	cpuCh  := make(chan collector.CPUSnapshot, 1)
	memCh  := make(chan collector.MemSnapshot, 1)
	procCh := make(chan []collector.ProcessInfo, 1)
	netCh  := make(chan collector.NetworkSnapshot, 1)
	inputCh := make(chan events.InputEvent, 5)
	stateCh := make(chan state.SystemState, 1)

	// wait group - to make sure all routines exit before main()
	// this makes for graceful exit and insures cleanup occurs.
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		collector.RunCPU(ctx, cpuCh, tickRate)
		log.Println("cpuCollector stopped")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		collector.RunMemory(ctx, memCh, tickRate)
		log.Println("memCollector stopped")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		collector.RunProcesses(ctx, procCh, tickRate)
		log.Println("processCollector stopped")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		collector.RunNetwork(ctx, netCh, tickRate)
		log.Println("networkCollector stopped")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		mgr := state.NewManager()
		mgr.Run(ctx, cpuCh, memCh, procCh, netCh, inputCh, stateCh)
		log.Println("stateManager stopped")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		handler := ui.NewInputHandler(inputCh, cancel) // cancel lets 'q' quit
		handler.Run(ctx)
		log.Println("inputHandler stopped")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		r := ui.NewRenderer()
		if err := r.Init(); err != nil {
			log.Fatalf("renderer init failed: %v", err)
		}
		defer r.Cleanup() // restore terminal state on exit
		r.Run(ctx, stateCh)
		log.Println("renderer stopped")
	}()

	// when context is cancelled, wait for goroutines to finish then exit
	<-ctx.Done()
	log.Println("context cancelled — waiting for goroutines to finish...")
	wg.Wait()
	log.Println("htop-lite exited cleanly")
}
