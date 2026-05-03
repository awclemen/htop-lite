////////////////////////////////////////////////////////////////////////////////
// Package events
//
// Purpose:
//   The events package defines the shared input-event types used by htop-lite.
//   These events are created by the UI input handler and consumed by the state
//   manager.
//
//   This package exists to keep communication between packages clean. Without
//   this separate package, the ui package and state package could end up needing
//   to import each other, which would cause an import cycle.
//
//   In simple terms:
//     - input.go reads a keypress.
//     - input.go turns that keypress into an InputEvent.
//     - manager.go receives the InputEvent.
//     - manager.go updates the program state based on that event.
//
// Package relationship:
//   ui/input.go  ---- sends InputEvent ---->  state/manager.go
//
// Language:
//   Go
//
// Deficiencies:
//   - Payload uses the type any, so the receiver must know what kind of data
//     to expect for each event.
//   - Invalid payload types are possible if events are constructed incorrectly.
//   - This package only describes user-input events, not collector updates.
////////////////////////////////////////////////////////////////////////////////

// Package events defines the InputEvent type shared between the ui and
// state packages. It exists solely to break the import cycle that would
// arise if ui defined InputEvent and state imported ui, or vice versa.
//
// Keeping shared message types in their own small package is idiomatic Go.
// The same pattern appears in the standard library, where small shared
// packages are sometimes used to keep larger packages independent.
package events

// EventType identifies the kind of action the user has requested.
//
// EventType is an int-based enum. Each constant below represents one possible
// user action from the terminal UI.
type EventType int

const (
	// EventScrollUp means the user wants to move the selected process upward.
	//
	// Triggered by:
	//   - Up arrow
	//   - k
	//
	// Expected Payload:
	//   - nil
	EventScrollUp EventType = iota

	// EventScrollDown means the user wants to move the selected process
	// downward.
	//
	// Triggered by:
	//   - Down arrow
	//   - j
	//
	// Expected Payload:
	//   - int containing the number of visible process rows
	//
	// The state manager uses that visible row count to decide when the process
	// list should scroll.
	EventScrollDown

	// EventCycleSort means the user wants to switch to the next sorting mode.
	//
	// Triggered by:
	//   - s
	//
	// Expected Payload:
	//   - nil
	//
	// The state manager cycles through sorting by CPU, memory, PID, and name.
	EventCycleSort

	// EventFilter means the user is typing or clearing a process-name filter.
	//
	// Triggered by:
	//   - /
	//   - printable characters while in filter mode
	//   - backspace while in filter mode
	//   - escape to clear/cancel the filter
	//
	// Expected Payload:
	//   - string containing the current filter query
	EventFilter

	// EventKill means the user wants to send a kill/interrupt signal to the
	// currently selected process.
	//
	// Triggered by:
	//   - x
	//
	// Expected Payload:
	//   - nil
	//
	// The state manager decides which process is selected and attempts to kill
	// that process.
	EventKill

	// EventQuit means the user wants to exit the program.
	//
	// Triggered by:
	//   - q
	//   - Q
	//   - Ctrl+C
	//   - Ctrl+Q
	//
	// Expected Payload:
	//   - nil
	//
	// The input handler also calls the shared cancel function so the whole
	// program can shut down cleanly.
	EventQuit
)

// InputEvent carries one user action from the input handler to the state
// manager.
//
// The Type field says what happened.
// The Payload field optionally carries extra information needed for that event.
//
// Payload types by EventType:
//   - EventScrollUp   -> nil
//   - EventScrollDown -> int
//   - EventCycleSort  -> nil
//   - EventFilter     -> string
//   - EventKill       -> nil
//   - EventQuit       -> nil
//
// The Payload field is typed as any so different event types can carry
// different extra data. The downside is that the state manager must type-check
// or type-assert the payload before using it.
type InputEvent struct {
	// Type identifies the kind of input action.
	Type EventType

	// Payload stores optional extra data for the event.
	//
	// Example:
	//   EventFilter uses Payload to store the current filter string.
	//   EventScrollDown uses Payload to store the number of visible rows.
	Payload any
}
