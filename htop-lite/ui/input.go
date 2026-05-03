package ui
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
// Package ui provides the terminal input handler and renderer for htop-lite.
//
// This file specifically contains the InputHandler. The InputHandler is
// responsible for reading keyboard input from the terminal and turning that
// input into events that the state manager can understand.
//
// In simple terms:
//   - The user presses a key.
//   - input.go reads that key.
//   - input.go converts the key into an InputEvent.
//   - manager.go receives the event and updates the program state.
//
// The input handler runs in its own goroutine so the program can keep
// collecting system data and drawing the UI while also listening for keys.
//
// Language:
//   Go
//
// External / Important Packages Used:
//   - context: used to coordinate shutdown across goroutines.
//   - log: used to write debug/status information to htop-lite.log.
//   - os/signal: used to detect Ctrl+C and other interrupt signals.
//   - golang.org/x/term - for interaction with the terminal
//
////////////////////////////////////////////////////////////////////////////////

import (
	"context"
	"log"
	"os"

	"golang.org/x/term"
	"htop-lite/events"
)

// Re-export event constants so callers that already import ui do not need
// to import the events package separately.
//
// These constants are aliases for the matching constants in events.go.
// They are not new event types; they simply point to the same values.
const (
	// EventScrollUp means the user wants to move the selected process upward.
	EventScrollUp = events.EventScrollUp

	// EventScrollDown means the user wants to move the selected process downward.
	EventScrollDown = events.EventScrollDown

	// EventCycleSort means the user wants to switch to the next sort mode.
	EventCycleSort = events.EventCycleSort

	// EventFilter means the user is typing or clearing a process-name filter.
	EventFilter = events.EventFilter

	// EventKill means the user wants to kill the currently selected process.
	EventKill = events.EventKill

	// EventQuit means the user wants to exit the program.
	EventQuit = events.EventQuit
)

// InputHandler reads raw keypresses from stdin and turns them into InputEvents.
//
// It owns terminal raw mode while it is running. Raw mode lets the program read
// single keypresses immediately instead of waiting for the user to press Enter.
//
// Fields:
//   - out sends InputEvents to the state manager.
//   - cancel shuts down the whole program when the user quits.
//   - filterMode tracks whether the user is typing a filter query.
//   - filterBuf stores the current filter text.
//   - visibleRows stores how many process rows are visible on screen.
type InputHandler struct {
	// out is the channel where keyboard events are sent.
	// The state manager receives from this channel.
	out chan<- events.InputEvent

	// cancel is the shared context cancellation function.
	// Calling this tells the rest of the program to shut down.
	cancel context.CancelFunc

	// filterMode is true when the user has pressed "/" and is typing a filter.
	filterMode bool

	// filterBuf stores the text typed while in filter mode.
	filterBuf string

	// visibleRows stores how many process rows are currently visible.
	//
	// The renderer can update this value so scroll-down events know how far
	// the visible process window extends.
	visibleRows int
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   NewInputHandler
//
// Purpose:
//   Creates and returns a new InputHandler. The handler sends keyboard events
//   through the provided output channel and uses the provided cancel function
//   to shut down the program when the user quits.
//
// Pre-conditions:
//   - out must be a valid InputEvent channel.
//   - cancel must be a valid context cancellation function.
//
// Post-conditions:
//   - Returns a pointer to a new InputHandler.
//   - The handler starts outside of filter mode.
//   - visibleRows is initialized to a safe default value.
//
// Parameters and information direction:
//   - out: output channel; sends InputEvent values to the state manager.
//   - cancel: input function; called when the user requests shutdown.
//   - returns: output; pointer to a new InputHandler.
////////////////////////////////////////////////////////////////////////////////
func NewInputHandler(out chan<- events.InputEvent, cancel context.CancelFunc) *InputHandler {
	return &InputHandler{
		out:         out,
		cancel:      cancel,
		visibleRows: 20, // safe default before renderer reports actual height
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   SetVisibleRows
//
// Purpose:
//   Updates the number of process rows currently visible in the terminal UI.
//   The renderer can call this after drawing so the input handler knows when a
//   downward scroll should move the scroll window.
//
// Pre-conditions:
//   - n should be greater than or equal to 1 for normal scrolling behavior.
//   - The renderer should call this with the current visible row count.
//
// Post-conditions:
//   - h.visibleRows is updated to n.
//
// Parameters and information direction:
//   - n: input; the number of process rows visible in the UI.
////////////////////////////////////////////////////////////////////////////////
func (h *InputHandler) SetVisibleRows(n int) {
	h.visibleRows = n
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   Run
//
// Purpose:
//   Runs the input handler's main loop. This method puts the terminal into raw
//   mode, reads keyboard input one keypress at a time, and passes those bytes to
//   handleBytes so they can be converted into InputEvents.
//
// Pre-conditions:
//   - ctx must be a valid context.
//   - h must be a properly initialized InputHandler.
//   - The program should be running in a real terminal.
//   - stdin must support terminal raw mode.
//
// Post-conditions:
//   - The terminal is placed into raw mode while this method runs.
//   - Keypresses are read from stdin and processed.
//   - The terminal is restored before the method exits.
//   - The method exits when ctx is cancelled or when reading from stdin fails.
//
// Parameters and information direction:
//   - ctx: input; controls when the input handler should stop.
////////////////////////////////////////////////////////////////////////////////
func (h *InputHandler) Run(ctx context.Context) {
	log.Println("inputHandler: started")

	// Get the file descriptor for standard input.
	// The terminal package needs this number to switch stdin into raw mode.
	fd := int(os.Stdin.Fd())

	// Put the terminal into raw mode.
	//
	// Normal terminal mode waits for Enter before sending input to the program.
	// Raw mode sends each keypress immediately, which is needed for an htop-like
	// interface.
	//
	// term.MakeRaw returns the old terminal state so we can restore it later.
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		log.Fatalf("inputHandler: failed to set raw mode: %v", err)
	}

	// Always restore the terminal when Run exits.
	//
	// This is very important. If raw mode is not restored, the user's terminal
	// can behave strangely after the program closes.
	defer func() {
		if err := term.Restore(fd, oldState); err != nil {
			log.Printf("inputHandler: failed to restore terminal: %v", err)
		}
		log.Println("inputHandler: terminal restored")
	}()

	// This buffer is large enough for normal single-byte keys and common
	// three-byte escape sequences like arrow keys.
	buf := make([]byte, 4)

	for {
		// Check whether the program has been asked to shut down.
		//
		// This lets the input handler exit when the user presses q, Ctrl+C,
		// or when another part of the program cancels the context.
		select {
		case <-ctx.Done():
			log.Println("inputHandler: context cancelled, exiting")
			return
		default:
		}

		// Read bytes from the terminal.
		//
		// In raw mode, this usually returns after a single keypress instead of
		// waiting for Enter.
		n, err := os.Stdin.Read(buf)
		if err != nil {
			log.Printf("inputHandler: read error: %v", err)
			return
		}

		// If nothing was read, loop around and try again.
		if n == 0 {
			continue
		}

		// Process only the bytes that were actually read.
		h.handleBytes(buf[:n])
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   handleBytes
//
// Purpose:
//   Interprets one keypress and converts it into an InputEvent. This method
//   handles two modes: normal mode and filter mode.
//
//   Normal mode:
//     - q quits.
//     - arrows or j/k scroll.
//     - s cycles sorting.
//     - x kills the selected process.
//     - / enters filter mode.
//
//   Filter mode:
//     - printable characters are added to the filter query.
//     - backspace removes one character.
//     - Enter confirms the current filter.
//     - Escape clears/cancels the filter.
//
// Pre-conditions:
//   - b should contain bytes read from stdin.
//   - h must have a valid output channel.
//   - h.filterMode tells this method which input mode is active.
//
// Post-conditions:
//   - May send an InputEvent to the state manager.
//   - May update h.filterMode.
//   - May update h.filterBuf.
//   - May call h.cancel if the user requests quit.
//
// Parameters and information direction:
//   - b: input; bytes representing the keypress read from the terminal.
////////////////////////////////////////////////////////////////////////////////
func (h *InputHandler) handleBytes(b []byte) {
	// -------------------------------------------------------------------------
	// Filter mode
	// -------------------------------------------------------------------------
	// Filter mode is active after the user presses "/".
	//
	// In this mode, normal letters/numbers/symbols are treated as search text
	// instead of commands.
	if h.filterMode {
		switch {
		case len(b) == 1 && b[0] == 27:
			// Escape key.
			//
			// Cancel filter mode and clear the current filter.
			h.filterMode = false
			h.filterBuf = ""
			h.send(events.InputEvent{Type: EventFilter, Payload: ""})

		case len(b) == 1 && (b[0] == 13 || b[0] == 10):
			// Enter key.
			//
			// Confirm the current filter and leave filter mode.
			// The filter text stays active because we do not clear filterBuf.
			h.filterMode = false

		case len(b) == 1 && (b[0] == 127 || b[0] == 8):
			// Backspace or Delete.
			//
			// Remove the last character from the filter buffer, then send the
			// updated query to the state manager.
			if len(h.filterBuf) > 0 {
				h.filterBuf = h.filterBuf[:len(h.filterBuf)-1]
			}
			h.send(events.InputEvent{Type: EventFilter, Payload: h.filterBuf})

		case len(b) == 1 && isPrintable(b[0]):
			// Normal printable character.
			//
			// Add it to the filter query and send the updated query.
			h.filterBuf += string(b[0])
			h.send(events.InputEvent{Type: EventFilter, Payload: h.filterBuf})
		}

		// Do not continue into normal command handling while in filter mode.
		return
	}

	// -------------------------------------------------------------------------
	// Normal mode
	// -------------------------------------------------------------------------
	// Normal mode treats keypresses as commands.
	switch {
	case len(b) == 1 && (b[0] == 'q' || b[0] == 'Q' || b[0] == 3 || b[0] == 17):
		// Quit command.
		//
		// Supported quit keys:
		//   - q
		//   - Q
		//   - Ctrl+C, byte value 3
		//   - Ctrl+Q, byte value 17
		log.Println("inputHandler: quit key pressed")
		h.send(events.InputEvent{Type: EventQuit})
		h.cancel()

	case isEscSeq(b, 'A') || (len(b) == 1 && b[0] == 'k'):
		// Scroll up.
		//
		// Up arrow is sent by the terminal as the escape sequence ESC [ A.
		// The letter k is also supported as a vim-style movement key.
		h.send(events.InputEvent{Type: EventScrollUp})

	case isEscSeq(b, 'B') || (len(b) == 1 && b[0] == 'j'):
		// Scroll down.
		//
		// Down arrow is sent by the terminal as the escape sequence ESC [ B.
		// The letter j is also supported as a vim-style movement key.
		//
		// The payload tells the state manager how many process rows are visible.
		h.send(events.InputEvent{Type: EventScrollDown, Payload: h.visibleRows})

	case len(b) == 1 && b[0] == 's':
		// Cycle the process sort mode.
		//
		// The state manager decides the actual order of sort modes.
		h.send(events.InputEvent{Type: EventCycleSort})

	case len(b) == 1 && b[0] == 'x':
		// Kill selected process.
		//
		// The input handler does not know which process is selected. It simply
		// tells the state manager that the user requested a kill action.
		h.send(events.InputEvent{Type: EventKill})

	case len(b) == 1 && b[0] == '/':
		// Enter filter mode.
		//
		// Clear the old filter buffer and immediately send an empty filter event
		// so the state manager and renderer know filtering has started/reset.
		h.filterMode = true
		h.filterBuf = ""
		h.send(events.InputEvent{Type: EventFilter, Payload: ""})
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   send
//
// Purpose:
//   Attempts to send an InputEvent to the state manager without blocking the
//   input handler. If the channel is full, the event is dropped and a message is
//   written to the log file.
//
// Pre-conditions:
//   - h.out must be a valid InputEvent channel.
//   - event should contain a valid EventType.
//   - Payload should match the expected type for that event.
//
// Post-conditions:
//   - If the channel has room, the event is sent.
//   - If the channel is full, the event is dropped.
//   - The input handler does not block waiting for the state manager.
//
// Parameters and information direction:
//   - event: input; the InputEvent to send to the state manager.
////////////////////////////////////////////////////////////////////////////////
func (h *InputHandler) send(event events.InputEvent) {
	select {
	case h.out <- event:
		// Event delivered successfully.
	default:
		// The input channel is full.
		//
		// Dropping an occasional input event is safer than freezing keyboard
		// input while waiting for the state manager.
		log.Printf("inputHandler: dropped event %v, channel full", event.Type)
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   isEscSeq
//
// Purpose:
//   Checks whether a byte slice matches a three-byte ANSI escape sequence.
//   This is mainly used to recognize arrow keys.
//
// Pre-conditions:
//   - b may contain any number of bytes.
//   - suffix should be the final byte expected in the escape sequence.
//
// Post-conditions:
//   - Returns true if b matches ESC [ suffix.
//   - Returns false otherwise.
//
// Parameters and information direction:
//   - b: input; bytes read from the terminal.
//   - suffix: input; expected final byte of the escape sequence.
//   - returns: output; true if the sequence matches.
////////////////////////////////////////////////////////////////////////////////
func isEscSeq(b []byte, suffix byte) bool {
	// Arrow keys usually arrive as three bytes:
	//   ESC = 0x1B
	//   [   = 0x5B
	//   A/B/C/D depending on the arrow key
	//
	// Up arrow    = ESC [ A
	// Down arrow  = ESC [ B
	// Right arrow = ESC [ C
	// Left arrow  = ESC [ D
	return len(b) == 3 && b[0] == 0x1B && b[1] == '[' && b[2] == suffix
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   isPrintable
//
// Purpose:
//   Checks whether a byte is a standard printable ASCII character. This is used
//   while typing a filter query so control characters do not get inserted into
//   the filter string.
//
// Pre-conditions:
//   - b may be any byte value.
//
// Post-conditions:
//   - Returns true if b is between ASCII 32 and ASCII 126.
//   - Returns false for control characters and non-standard bytes.
//
// Parameters and information direction:
//   - b: input; byte to check.
//   - returns: output; true if the byte is printable ASCII.
////////////////////////////////////////////////////////////////////////////////
func isPrintable(b byte) bool {
	return b >= 32 && b <= 126
}
