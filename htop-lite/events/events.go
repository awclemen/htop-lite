// Package events defines the InputEvent type shared between the ui and
// state packages. It exists solely to break the import cycle that would
// arise if ui defined InputEvent and state imported ui (or vice versa).
//
// Keeping shared message types in their own small package is idiomatic
// Go — the same pattern appears in the standard library (e.g. net/http
// and net/http/internal).
package events

// EventType identifies the kind of action the user has requested.
type EventType int

const (
	EventScrollUp   EventType = iota // ↑ arrow or k
	EventScrollDown                  // ↓ arrow or j
	EventCycleSort                   // s — advance sort column
	EventFilter                      // / — update filter query
	EventKill                        // x     — SIGTERM selected process
	EventQuit                        // q or Ctrl+C — exit
)

// InputEvent carries a user action and an optional payload.
//
// Payload types by EventType:
//   - EventScrollDown → int  (current visible row count)
//   - EventFilter     → string (current filter query)
//   - all others      → nil
type InputEvent struct {
	Type    EventType
	Payload any
}
