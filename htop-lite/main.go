package main

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
//   htop-lite is a miniature version of htop that runs directly in the
//   terminal. It collects live system information such as CPU usage, memory
//   usage, network activity, and running processes. The program displays that
//   information in a terminal UI and allows the user to scroll, sort, filter,
//   kill processes, and quit cleanly.
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

// tickRate controls how often the collectors gather new system data.
// In this program, CPU, memory, process, and network data are all refreshed
// once per second.
const tickRate = time.Second

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   main
//
// Purpose:
//   Starts and manages the entire htop-lite application. This function sets up
//   logging, creates the shared cancellation context, creates all communication
//   channels, launches the collector/state/UI goroutines, and waits for the
//   program to shut down cleanly.
//
// Pre-conditions:
//   - The program must be run from a terminal.
//   - The needed project packages must be available.
//   - The program must have permission to create or append to htop-lite.log.
//   - Some system information may require normal OS read permissions.
//
// Post-conditions:
//   - All collectors, the state manager, input handler, and renderer are started.
//   - The program continues running until the user presses q, Ctrl+C, or the
//     process receives SIGTERM.
//   - On shutdown, all goroutines are given a chance to stop cleanly.
//   - The log file records startup, shutdown, and goroutine exit messages.
//
// Parameters and information direction:
//   - main has no parameters.
//   - main does not return a value.
//   - Information flows through channels between goroutines:
//       * collector channels send system snapshots to the state manager.
//       * inputCh sends keyboard events to the state manager.
//       * stateCh sends complete SystemState snapshots to the renderer.
////////////////////////////////////////////////////////////////////////////////
func main() {
	// -------------------------------------------------------------------------
	// Logging setup
	// -------------------------------------------------------------------------
	// The program draws directly to the terminal, so normal log messages would
	// mess up the UI if they printed to the screen.
	//
	// Instead, all log output is redirected into htop-lite.log.
	//
	// os.O_CREATE means the file is created if it does not already exist.
	// os.O_WRONLY means the file is opened for writing.
	// os.O_APPEND means new logs are added to the end instead of replacing
	// old logs.
	// 0644 gives the owner read/write permission and everyone else read-only.
	logFile, err := os.OpenFile("htop-lite.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// log.Fatalf prints the error and immediately exits the program.
		// If logging cannot be set up, the program stops because logging is
		// important for debugging terminal UI behavior.
		log.Fatalf("failed to open log file: %v", err)
	}

	// Make sure the log file is closed when main exits.
	defer logFile.Close()

	// Send all log output to the log file instead of the terminal.
	log.SetOutput(logFile)
	log.Println("htop-lite starting...")

	// -------------------------------------------------------------------------
	// Context setup
	// -------------------------------------------------------------------------
	// context.WithCancel creates a context that can be shared with every
	// goroutine. When cancel() is called, every goroutine watching ctx.Done()
	// knows it should stop.
	//
	// This is the main shutdown signal for the whole program.
	ctx, cancel := context.WithCancel(context.Background())

	// If main exits for any reason, call cancel() to make sure goroutines are
	// told to stop.
	defer cancel()

	// -------------------------------------------------------------------------
	// OS signal handling
	// -------------------------------------------------------------------------
	// sigCh receives operating system signals, such as Ctrl+C.
	// The channel is buffered with size 1 so the signal package can deliver
	// one signal even if the goroutine has not read it yet.
	sigCh := make(chan os.Signal, 1)

	// Tell Go to send SIGINT and SIGTERM signals into sigCh.
	//
	// SIGINT usually happens when the user presses Ctrl+C.
	// SIGTERM usually means the operating system or another process asked this
	// program to shut down.
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// This goroutine waits for an interrupt/termination signal.
	// When one arrives, it logs the signal and cancels the shared context.
	go func() {
		sig := <-sigCh
		log.Printf("received signal: %v — shutting down", sig)
		cancel()
	}()

	// -------------------------------------------------------------------------
	// Channel setup
	// -------------------------------------------------------------------------
	// Each collector has its own channel so different types of system data stay
	// separate and strongly typed.
	//
	// Most collector channels have buffer size 1. This means the collector can
	// place one fresh value in the channel without blocking. If the state manager
	// is slightly behind, the program can drop/replace old data instead of
	// building up a long queue of stale system snapshots.
	cpuCh := make(chan collector.CPUSnapshot, 1)
	memCh := make(chan collector.MemSnapshot, 1)
	procCh := make(chan []collector.ProcessInfo, 1)
	netCh := make(chan collector.NetworkSnapshot, 1)

	// inputCh carries keyboard input events from the input handler to the
	// state manager.
	//
	// It has a slightly larger buffer because a user can press keys quickly.
	inputCh := make(chan events.InputEvent, 5)

	// stateCh carries the complete current SystemState from the state manager
	// to the renderer. The renderer uses this to redraw the terminal UI.
	stateCh := make(chan state.SystemState, 1)

	// -------------------------------------------------------------------------
	// WaitGroup setup
	// -------------------------------------------------------------------------
	// A WaitGroup lets main wait until all goroutines have finished before the
	// program exits.
	//
	// Each goroutine calls wg.Done() when it stops.
	var wg sync.WaitGroup

	// -------------------------------------------------------------------------
	// CPU collector goroutine
	// -------------------------------------------------------------------------
	// This goroutine samples CPU data once per tickRate and sends CPUSnapshot
	// values into cpuCh.
	wg.Add(1)
	go func() {
		defer wg.Done()

		collector.RunCPU(ctx, cpuCh, tickRate)

		// This log line runs only after RunCPU exits.
		log.Println("cpuCollector stopped")
	}()

	// -------------------------------------------------------------------------
	// Memory collector goroutine
	// -------------------------------------------------------------------------
	// This goroutine samples RAM/swap information once per tickRate and sends
	// MemSnapshot values into memCh.
	wg.Add(1)
	go func() {
		defer wg.Done()

		collector.RunMemory(ctx, memCh, tickRate)

		// This log line runs only after RunMemory exits.
		log.Println("memCollector stopped")
	}()

	// -------------------------------------------------------------------------
	// Process collector goroutine
	// -------------------------------------------------------------------------
	// This goroutine gathers the current process list once per tickRate and
	// sends a slice of ProcessInfo values into procCh.
	wg.Add(1)
	go func() {
		defer wg.Done()

		collector.RunProcesses(ctx, procCh, tickRate)

		// This log line runs only after RunProcesses exits.
		log.Println("processCollector stopped")
	}()

	// -------------------------------------------------------------------------
	// Network collector goroutine
	// -------------------------------------------------------------------------
	// This goroutine gathers network upload/download information once per
	// tickRate and sends NetworkSnapshot values into netCh.
	wg.Add(1)
	go func() {
		defer wg.Done()

		collector.RunNetwork(ctx, netCh, tickRate)

		// This log line runs only after RunNetwork exits.
		log.Println("networkCollector stopped")
	}()

	// -------------------------------------------------------------------------
	// State manager goroutine
	// -------------------------------------------------------------------------
	// The state manager is the middle layer of the program.
	//
	// It receives:
	//   - CPU snapshots from cpuCh
	//   - memory snapshots from memCh
	//   - process lists from procCh
	//   - network snapshots from netCh
	//   - keyboard input events from inputCh
	//
	// Then it combines everything into one SystemState and sends that to the
	// renderer through stateCh.
	wg.Add(1)
	go func() {
		defer wg.Done()

		mgr := state.NewManager()
		mgr.Run(ctx, cpuCh, memCh, procCh, netCh, inputCh, stateCh)

		// This log line runs only after the state manager exits.
		log.Println("stateManager stopped")
	}()

	// -------------------------------------------------------------------------
	// Input handler goroutine
	// -------------------------------------------------------------------------
	// The input handler reads raw keyboard input from the terminal.
	//
	// It converts keys like q, arrows, s, /, and x into InputEvent values and
	// sends them through inputCh.
	//
	// The cancel function is passed in so the input handler can shut down the
	// whole program when the user presses q.
	wg.Add(1)
	go func() {
		defer wg.Done()

		handler := ui.NewInputHandler(inputCh, cancel)
		handler.Run(ctx)

		// This log line runs only after the input handler exits.
		log.Println("inputHandler stopped")
	}()

	// -------------------------------------------------------------------------
	// Renderer goroutine
	// -------------------------------------------------------------------------
	// The renderer owns the terminal display.
	//
	// It receives SystemState values from stateCh and redraws the terminal UI.
	// It also hides/restores the cursor and cleans up the terminal when the
	// program exits.
	wg.Add(1)
	go func() {
		defer wg.Done()

		r := ui.NewRenderer()

		// Initialize the renderer before starting the draw loop.
		// This usually clears the screen and hides the cursor.
		if err := r.Init(); err != nil {
			log.Fatalf("renderer init failed: %v", err)
		}

		// Cleanup restores the terminal state when the renderer exits.
		// This is important because terminal UI programs can leave the terminal
		// looking weird if they do not restore it properly.
		defer r.Cleanup()

		// Start the renderer loop. It runs until ctx is cancelled.
		r.Run(ctx, stateCh)

		// This log line runs only after the renderer exits.
		log.Println("renderer stopped")
	}()

	// -------------------------------------------------------------------------
	// Main shutdown wait
	// -------------------------------------------------------------------------
	// main blocks here until the shared context is cancelled.
	//
	// The context can be cancelled by:
	//   - the user pressing q
	//   - the user pressing Ctrl+C
	//   - the OS sending SIGTERM
	<-ctx.Done()

	log.Println("context cancelled — waiting for goroutines to finish...")

	// Wait for all goroutines to finish before letting main return.
	// This prevents the program from exiting while a goroutine is still trying
	// to write logs, restore the terminal, or finish cleanup work.
	wg.Wait()

	log.Println("htop-lite exited cleanly")
}
