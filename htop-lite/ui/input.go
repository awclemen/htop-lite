// Package ui provides the terminal input handler and renderer for htop-lite.
// The input handler runs in its own goroutine, reads raw keypresses from
// stdin, and translates them into InputEvents that the state manager acts on.
package ui

import (
	"context"
	"log"
	"os"

	"golang.org/x/term"
	"htop-lite/events"
)

// Re-export event constants so callers that already import ui don't need
// a second import just for the constant names.
const (
	EventScrollUp   = events.EventScrollUp
	EventScrollDown = events.EventScrollDown
	EventCycleSort  = events.EventCycleSort
	EventFilter     = events.EventFilter
	EventKill       = events.EventKill
	EventQuit       = events.EventQuit
)

// InputHandler reads raw keypresses from stdin and emits InputEvents.
// It owns the terminal's raw mode for the duration of its lifetime.
type InputHandler struct {
	out        chan<- events.InputEvent
	cancel     context.CancelFunc
	filterMode bool   // true while the user is typing a filter query
	filterBuf  string // accumulates keystrokes in filter mode

	// visibleRows is updated by the renderer via SetVisibleRows so that
	// scroll-down events can carry the correct row count as payload.
	visibleRows int
}

// NewInputHandler constructs an InputHandler that sends events on out.
// cancel is called when the user presses q or Ctrl+C, triggering a
// clean shutdown of the entire program.
func NewInputHandler(out chan<- events.InputEvent, cancel context.CancelFunc) *InputHandler {
	return &InputHandler{
		out:         out,
		cancel:      cancel,
		visibleRows: 20, // safe default before the renderer reports actual height
	}
}

// SetVisibleRows lets the renderer inform the input handler how many
// process rows are currently visible. This is used as the scroll-down
// payload so the state manager can advance the scroll window correctly.
// Safe to call from the renderer goroutine — visibleRows is written once
// per frame and read once per keypress, and both are infrequent enough
// that a torn read would just cause a one-frame scroll glitch.
func (h *InputHandler) SetVisibleRows(n int) {
	h.visibleRows = n
}

// Run is the input handler's main loop. It puts the terminal into raw
// mode, then reads one byte at a time, translating escape sequences and
// printable characters into InputEvents.
//
// It exits when ctx is cancelled (e.g. after the user presses q).
func (h *InputHandler) Run(ctx context.Context) {
	log.Println("inputHandler: started")

	// Switch stdin to raw mode so we receive individual keypresses
	// immediately, without waiting for the user to press Enter.
	// term.MakeRaw returns the previous terminal state so we can restore
	// it when we exit — critical for leaving the user's shell intact.
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		log.Fatalf("inputHandler: failed to set raw mode: %v", err)
	}
	defer func() {
		if err := term.Restore(fd, oldState); err != nil {
			log.Printf("inputHandler: failed to restore terminal: %v", err)
		}
		log.Println("inputHandler: terminal restored")
	}()

	buf := make([]byte, 4) // large enough for 3-byte escape sequences

	for {
		// Check for cancellation before each blocking read.
		select {
		case <-ctx.Done():
			log.Println("inputHandler: context cancelled, exiting")
			return
		default:
		}

		n, err := os.Stdin.Read(buf)
		if err != nil {
			log.Printf("inputHandler: read error: %v", err)
			return
		}
		if n == 0 {
			continue
		}

		h.handleBytes(buf[:n])
	}
}

// handleBytes interprets one keypress (which may be 1–3 bytes for escape
// sequences) and either accumulates a filter query or emits an InputEvent.
func (h *InputHandler) handleBytes(b []byte) {
	// ------------------------------------------------------------------
	// Filter mode — every printable character appends to the query.
	// Escape or Enter exits filter mode; Backspace removes last char.
	// ------------------------------------------------------------------
	if h.filterMode {
		switch {
		case len(b) == 1 && b[0] == 27: // Escape — cancel filter
			h.filterMode = false
			h.filterBuf = ""
			h.send(events.InputEvent{Type: EventFilter, Payload: ""})

		case len(b) == 1 && (b[0] == 13 || b[0] == 10): // Enter — confirm filter
			h.filterMode = false
			// Keep the filter query active; don't clear filterBuf.
			// The user can press / again to refine it.

		case len(b) == 1 && (b[0] == 127 || b[0] == 8): // Backspace / DEL
			if len(h.filterBuf) > 0 {
				h.filterBuf = h.filterBuf[:len(h.filterBuf)-1]
			}
			h.send(events.InputEvent{Type: EventFilter, Payload: h.filterBuf})

		case len(b) == 1 && isPrintable(b[0]):
			h.filterBuf += string(b[0])
			h.send(events.InputEvent{Type: EventFilter, Payload: h.filterBuf})
		}
		return
	}

	// ------------------------------------------------------------------
	// Normal mode — map keypresses to events.
	// ------------------------------------------------------------------
	switch {
	// Quit: q or Ctrl+C (0x03) or Ctrl+Q (0x11)
	case len(b) == 1 && (b[0] == 'q' || b[0] == 'Q' || b[0] == 3 || b[0] == 17):
		log.Println("inputHandler: quit key pressed")
		h.send(events.InputEvent{Type: EventQuit})
		h.cancel()

	// Scroll up: ↑ arrow (ESC [ A) or k (vim-style)
	case isEscSeq(b, 'A') || (len(b) == 1 && b[0] == 'k'):
		h.send(events.InputEvent{Type: EventScrollUp})

	// Scroll down: ↓ arrow (ESC [ B) or j (vim-style)
	case isEscSeq(b, 'B') || (len(b) == 1 && b[0] == 'j'):
		h.send(events.InputEvent{Type: EventScrollDown, Payload: h.visibleRows})

	// Cycle sort: s
	case len(b) == 1 && b[0] == 's':
		h.send(events.InputEvent{Type: EventCycleSort})

	// Kill selected: x or F9 (ESC [ 2 0 ~, but support x for simplicity)
	case len(b) == 1 && b[0] == 'x':
		h.send(events.InputEvent{Type: EventKill})

	// Enter filter mode: /
	case len(b) == 1 && b[0] == '/':
		h.filterMode = true
		h.filterBuf = ""
		h.send(events.InputEvent{Type: EventFilter, Payload: ""})
	}
}

// send attempts a non-blocking send on the output channel.
// If the state manager's input channel is full (it's processing a previous
// event), the new event is dropped. At 1-second tick rates this is
// vanishingly rare and dropping a single scroll event is harmless.
func (h *InputHandler) send(event events.InputEvent) {
	select {
	case h.out <- event:
	default:
		log.Printf("inputHandler: dropped event %v, channel full", event.Type)
	}
}

// isEscSeq returns true if b is the 3-byte ANSI escape sequence ESC [ suffix.
// Arrow keys are encoded as: ESC (0x1B), [ (0x5B), then A/B/C/D.
func isEscSeq(b []byte, suffix byte) bool {
	return len(b) == 3 && b[0] == 0x1B && b[1] == '[' && b[2] == suffix
}

// isPrintable returns true for standard printable ASCII characters (32–126).
// This guards the filter buffer against control characters being typed in.
func isPrintable(b byte) bool {
	return b >= 32 && b <= 126
}
