package state

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
//   manager.go contains the state manager for htop-lite. The state manager is
//   the middle layer between the system collectors, the input handler, and the
//   renderer. It receives CPU, memory, process, network, and keyboard input
//   updates through channels. It combines those updates into one SystemState
//   struct and sends that state to the renderer.
//
//   This file is also responsible for process sorting, filtering, scrolling,
//   selection tracking, and sending kill requests to the selected process.
//
// Language:
//   Go
//
// Important Packages Used:
//   - context: lets the manager stop when the main program is shutting down.
//   - log: writes manager activity and errors to htop-lite.log.
//   - sort: sorts the process list by CPU, memory, PID, or name.
//   - strings: supports case-insensitive filtering and name sorting.
//
// Deficiencies:
//   - EventScrollDown assumes its Payload is an int.
//   - The kill feature depends on operating system permissions.
//   - Sorting and filtering happen in memory on the full process list.
//   - The state manager currently logs quit events but shutdown is mainly
//     handled through context cancellation from main/input.
////////////////////////////////////////////////////////////////////////////////

import (
	"context"
	"log"
	"sort"
	"strings"

	"htop-lite/collector"
	"htop-lite/events"
)

// SortField represents the process-table column currently being used for
// sorting.
//
// The renderer uses this value to highlight the active sort column, and the
// state manager uses it to decide how the process list should be ordered.
type SortField int

const (
	// SortByCPU sorts processes by CPU usage, highest first.
	// This is the default because htop-style programs usually show the busiest
	// processes at the top.
	SortByCPU SortField = iota

	// SortByMem sorts processes by memory percentage, highest first.
	SortByMem

	// SortByPID sorts processes by process ID, lowest first.
	SortByPID

	// SortByName sorts processes alphabetically by process name.
	SortByName
)

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   HRLabel
//
// Purpose:
//   Converts a SortField value into the human-readable label used by the
//   renderer. This makes the internal enum value displayable in the terminal UI.
//
// Pre-conditions:
//   - s should be one of the defined SortField constants.
//   - If s is not recognized, the method safely falls back to "CPU%".
//
// Post-conditions:
//   - Returns a string label matching the table column name.
//
// Parameters and information direction:
//   - s: input; the current SortField value.
//   - returns: output; the label to display in the UI.
////////////////////////////////////////////////////////////////////////////////
func (s SortField) HRLabel() string {
	switch s {
	case SortByCPU:
		return "CPU%"
	case SortByMem:
		return "MEM%"
	case SortByPID:
		return "PID"
	case SortByName:
		return "NAME"
	default:
		// CPU is the default sort mode, so it is also the safest fallback label.
		return "CPU%"
	}
}

// SystemState stores one complete snapshot of everything the UI needs to draw.
//
// The collectors send separate pieces of information, but the renderer wants
// one combined state object. The manager owns this struct and updates it as new
// information arrives.
type SystemState struct {
	// CPU holds the most recent CPU snapshot from the CPU collector.
	CPU collector.CPUSnapshot

	// Memory holds the most recent memory snapshot from the memory collector.
	Memory collector.MemSnapshot

	// Processes holds the filtered and sorted process list that is ready for
	// the renderer to display. This is not necessarily the raw full process list.
	Processes []collector.ProcessInfo

	// Network holds the most recent network snapshot from the network collector.
	Network collector.NetworkSnapshot

	// SortBy stores the current sort mode.
	SortBy SortField

	// FilterQuery stores the current process-name search/filter text.
	FilterQuery string

	// ScrollOffset is the index of the first process currently visible in the
	// scrolling process table.
	ScrollOffset int

	// Selected is the index of the currently selected process inside the
	// filtered/sorted process list.
	Selected int
}

// Manager owns and updates the current SystemState.
//
// current is what the renderer receives.
// rawProcesses keeps the unfiltered process list from the process collector so
// filtering can be recalculated whenever the filter query changes.
type Manager struct {
	current      SystemState
	rawProcesses []collector.ProcessInfo
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   NewManager
//
// Purpose:
//   Creates and returns a new Manager with a default starting state.
//
// Pre-conditions:
//   - No pre-existing Manager is required.
//   - The caller should use the returned pointer to start Run().
//
// Post-conditions:
//   - Returns a Manager pointer.
//   - The manager starts with SortByCPU as its default sorting mode.
//   - Other fields begin at their zero values until collector data arrives.
//
// Parameters and information direction:
//   - No parameters.
//   - returns: output; pointer to a new Manager.
////////////////////////////////////////////////////////////////////////////////
func NewManager() *Manager {
	return &Manager{
		current: SystemState{
			// Default to CPU sorting so the busiest processes appear first.
			SortBy: SortByCPU,
		},
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   Run
//
// Purpose:
//   Runs the main state-manager loop. This method continuously waits for new
//   collector snapshots or input events. Whenever something changes, it updates
//   the current SystemState and broadcasts the newest state to the renderer.
//
// Pre-conditions:
//   - ctx must be a valid context.
//   - All collector/input/state channels must be created before Run is called.
//   - The channel types must match the expected snapshot or event type.
//   - Run is intended to be called inside its own goroutine.
//
// Post-conditions:
//   - The manager updates m.current as new data arrives.
//   - The manager broadcasts updated SystemState snapshots to stateCh.
//   - The method exits cleanly when ctx is cancelled.
//
// Parameters and information direction:
//   - ctx: input; tells the manager when to stop.
//   - cpuCh: input; receives CPU snapshots from the CPU collector.
//   - memCh: input; receives memory snapshots from the memory collector.
//   - procCh: input; receives process lists from the process collector.
//   - netCh: input; receives network snapshots from the network collector.
//   - inputCh: input; receives keyboard actions from the input handler.
//   - stateCh: output; sends complete SystemState snapshots to the renderer.
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) Run(
	ctx context.Context,
	cpuCh <-chan collector.CPUSnapshot,
	memCh <-chan collector.MemSnapshot,
	procCh <-chan []collector.ProcessInfo,
	netCh <-chan collector.NetworkSnapshot,
	inputCh <-chan events.InputEvent,
	stateCh chan SystemState,
) {
	log.Println("stateManager: started")

	for {
		select {
		case <-ctx.Done():
			// The main program or input handler has requested shutdown.
			log.Println("stateManager: context cancelled, exiting")
			return

		case snap := <-cpuCh:
			// Store the newest CPU reading and tell the renderer to redraw.
			m.current.CPU = snap
			m.broadcast(stateCh)

		case snap := <-memCh:
			// Store the newest memory reading and tell the renderer to redraw.
			m.current.Memory = snap
			m.broadcast(stateCh)

		case procs := <-procCh:
			// Save the raw process list first.
			// Then rebuild the displayed process list using the current filter
			// and current sort mode.
			m.rawProcesses = procs
			m.applyFilter()
			m.applySort()
			m.clampScroll()
			m.broadcast(stateCh)

		case snap := <-netCh:
			// Store the newest network reading and tell the renderer to redraw.
			m.current.Network = snap
			m.broadcast(stateCh)

		case event := <-inputCh:
			// Keyboard input changes UI state, such as scrolling, sorting,
			// filtering, or killing a selected process.
			m.handleInput(event, ctx)
			m.broadcast(stateCh)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   handleInput
//
// Purpose:
//   Applies one user input event to the current state. This method handles
//   scrolling, sort cycling, filtering, process killing, and quit events.
//
// Pre-conditions:
//   - event should contain one of the EventType values from the events package.
//   - EventScrollDown should include an int payload for the visible row count.
//   - EventFilter should include a string payload for the filter query.
//   - The process list may be empty, so bounds checks are required.
//
// Post-conditions:
//   - m.current may be updated depending on the event.
//   - Sorting/filtering may be recalculated.
//   - The selected process may receive a kill signal for EventKill.
//   - Quit events are logged.
//
// Parameters and information direction:
//   - event: input; describes the user action to apply.
//   - ctx: input; currently passed in but not directly used by this method.
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) handleInput(event events.InputEvent, ctx context.Context) {
	switch event.Type {

	case events.EventScrollUp:
		// Move the selected row up if we are not already at the first row.
		if m.current.Selected > 0 {
			m.current.Selected--
		}

		// If the selected row moved above the visible window, scroll the
		// window upward too.
		if m.current.Selected < m.current.ScrollOffset {
			m.current.ScrollOffset--
		}

	case events.EventScrollDown:
		// maxIdx is the final valid process index.
		maxIdx := len(m.current.Processes) - 1

		// Move the selected row down if there is another process below it.
		if m.current.Selected < maxIdx {
			m.current.Selected++
		}

		// The input handler sends the number of visible process rows as the
		// payload. The manager uses it to decide when scrolling is needed.
		visibleRows := event.Payload.(int)

		// If the selected row moved below the visible window, scroll the
		// window downward too.
		if m.current.Selected >= m.current.ScrollOffset+visibleRows {
			m.current.ScrollOffset++
		}

	case events.EventCycleSort:
		// Move to the next sort mode.
		// There are four modes: CPU, memory, PID, and name.
		m.current.SortBy = SortField((int(m.current.SortBy) + 1) % 4)

		// Reset selection and scrolling so the user starts at the top of the
		// newly sorted list.
		m.current.Selected = 0
		m.current.ScrollOffset = 0

		// Re-sort the current process list using the new mode.
		m.applySort()

	case events.EventFilter:
		// Filter events should carry the current query as a string.
		// If the payload is not a string for any reason, fall back to no filter.
		query, ok := event.Payload.(string)
		if !ok {
			query = ""
		}

		// Store the query, reset list position, and rebuild the visible process
		// list from the raw process list.
		m.current.FilterQuery = query
		m.current.Selected = 0
		m.current.ScrollOffset = 0
		m.applyFilter()
		m.applySort()
		m.clampScroll()

	case events.EventKill:
		// Send a signal to the currently selected process.
		m.killSelected()

	case events.EventQuit:
		// Shutdown is mainly handled by context cancellation elsewhere.
		// The manager logs that it received the quit event.
		log.Println("stateManager: quit event received")
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   applyFilter
//
// Purpose:
//   Builds the displayed process list based on the current filter query.
//   If the filter query is empty, all raw processes are displayed. If the query
//   is not empty, only processes whose names contain the query are displayed.
//
// Pre-conditions:
//   - m.rawProcesses should contain the most recent full process list.
//   - m.current.FilterQuery should contain the desired filter text.
//   - Process names should be valid strings, though they may be empty.
//
// Post-conditions:
//   - m.current.Processes is replaced with the filtered process list.
//   - Filtering is case-insensitive.
//   - The raw process list is not changed.
//
// Parameters and information direction:
//   - No parameters.
//   - Reads from m.rawProcesses and m.current.FilterQuery.
//   - Writes to m.current.Processes.
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) applyFilter() {
	// Normalize the query so filtering ignores capitalization and extra spaces.
	query := strings.ToLower(strings.TrimSpace(m.current.FilterQuery))

	if query == "" {
		// No filter means every process should be displayed.
		//
		// Make a fresh copy instead of assigning the slice directly. This keeps
		// the displayed list separate from the raw list so later sorting does
		// not accidentally reorder rawProcesses.
		m.current.Processes = make([]collector.ProcessInfo, len(m.rawProcesses))
		copy(m.current.Processes, m.rawProcesses)
		return
	}

	// Reuse the existing backing array when possible to avoid extra allocation.
	filtered := m.current.Processes[:0]

	for _, p := range m.rawProcesses {
		// Match process names case-insensitively.
		if strings.Contains(strings.ToLower(p.Name), query) {
			filtered = append(filtered, p)
		}
	}

	m.current.Processes = filtered
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   applySort
//
// Purpose:
//   Sorts the current displayed process list according to m.current.SortBy.
//   This method only sorts m.current.Processes, which has already been filtered.
//
// Pre-conditions:
//   - m.current.Processes should contain the current visible process list.
//   - m.current.SortBy should contain one of the SortField constants.
//
// Post-conditions:
//   - m.current.Processes is reordered in place.
//   - CPU and memory sorts put highest usage first.
//   - PID sort puts lowest PID first.
//   - Name sort is alphabetical and case-insensitive.
//
// Parameters and information direction:
//   - No parameters.
//   - Reads m.current.SortBy.
//   - Mutates m.current.Processes.
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) applySort() {
	procs := m.current.Processes

	switch m.current.SortBy {
	case SortByCPU:
		// Highest CPU usage should appear first.
		sort.Slice(procs, func(i, j int) bool {
			return procs[i].CPUPercent > procs[j].CPUPercent
		})

	case SortByMem:
		// Highest memory usage should appear first.
		sort.Slice(procs, func(i, j int) bool {
			return procs[i].MemPercent > procs[j].MemPercent
		})

	case SortByPID:
		// Lower process IDs should appear first.
		sort.Slice(procs, func(i, j int) bool {
			return procs[i].PID < procs[j].PID
		})

	case SortByName:
		// Sort alphabetically by process name.
		// Convert both names to lowercase so capitalization does not affect order.
		sort.Slice(procs, func(i, j int) bool {
			return strings.ToLower(procs[i].Name) < strings.ToLower(procs[j].Name)
		})
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   clampScroll
//
// Purpose:
//   Keeps the selected index and scroll offset inside the valid range of the
//   current process list. This prevents the UI from trying to display or select
//   a process that no longer exists after filtering or process-list updates.
//
// Pre-conditions:
//   - m.current.Processes may contain zero or more processes.
//   - m.current.Selected and m.current.ScrollOffset may be stale because the
//     process list may have changed.
//
// Post-conditions:
//   - If there are no processes, Selected and ScrollOffset become 0.
//   - Selected is never greater than the last valid process index.
//   - ScrollOffset is not allowed to be below the selected row.
//
// Parameters and information direction:
//   - No parameters.
//   - Reads len(m.current.Processes).
//   - Updates m.current.Selected and m.current.ScrollOffset if needed.
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) clampScroll() {
	total := len(m.current.Processes)

	if total == 0 {
		// Nothing is available to select or scroll through.
		m.current.Selected = 0
		m.current.ScrollOffset = 0
		return
	}

	if m.current.Selected >= total {
		// If the selected process disappeared, move selection to the last
		// available process.
		m.current.Selected = total - 1
	}

	if m.current.ScrollOffset > m.current.Selected {
		// If the visible window starts after the selected row, pull the window
		// back so the selected row is visible again.
		m.current.ScrollOffset = m.current.Selected
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   killSelected
//
// Purpose:
//   Sends a termination signal to the currently selected process. This is used
//   when the user presses the kill key in the UI.
//
// Pre-conditions:
//   - m.current.Processes may be empty.
//   - m.current.Selected should point to a valid process, but this method checks
//     bounds to avoid invalid access.
//   - The operating system may deny permission to kill some processes.
//
// Post-conditions:
//   - If a valid selected process exists, the manager attempts to signal it.
//   - Success or failure is written to the log file.
//   - The process list itself is not immediately modified here. The collector
//     will refresh the list on a future tick.
//
// Parameters and information direction:
//   - No parameters.
//   - Reads m.current.Processes and m.current.Selected.
//   - Calls Kill on the selected ProcessInfo.
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) killSelected() {
	procs := m.current.Processes

	if len(procs) == 0 {
		// There is nothing to kill.
		return
	}

	idx := m.current.Selected

	if idx < 0 || idx >= len(procs) {
		// Safety check in case the selected index is stale or invalid.
		return
	}

	target := procs[idx]
	log.Printf("stateManager: sending SIGTERM to PID %d (%s)", target.PID, target.Name)

	if err := target.Kill(); err != nil {
		// Killing can fail if the process already exited or if the user does
		// not have permission.
		log.Printf("stateManager: kill PID %d failed: %v", target.PID, err)
	} else {
		log.Printf("stateManager: killed PID %d (%s)", target.PID, target.Name)
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   broadcast
//
// Purpose:
//   Sends a copy of the current SystemState to the renderer. If the renderer is
//   behind and the channel already contains an old state, this method removes
//   the stale state and tries to send the newest one instead.
//
// Pre-conditions:
//   - stateCh must be a valid SystemState channel.
//   - m.current should contain the most recent state known to the manager.
//   - stateCh is expected to be buffered, usually with capacity 1.
//
// Post-conditions:
//   - The newest state is sent to stateCh if possible.
//   - If the renderer is still busy, older stale state may be dropped.
//   - The manager does not block waiting for the renderer.
//
// Parameters and information direction:
//   - stateCh: output; receives the newest SystemState snapshot.
//   - m.current: input; copied into a local snapshot before sending.
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) broadcast(stateCh chan SystemState) {
	// Copy the current state before sending it.
	// This avoids the renderer receiving a partially changed state while the
	// manager continues updating m.current.
	snap := m.current

	select {
	case stateCh <- snap:
		// The renderer was ready, so the state was sent successfully.
	default:
		// The channel already has an older snapshot.
		// Drain that stale snapshot first.
		select {
		case <-stateCh:
		default:
		}

		// Try one more time to send the fresh snapshot.
		select {
		case stateCh <- snap:
		default:
			// If this still fails, the renderer is very busy. Drop this frame
			// instead of blocking the state manager.
			log.Println("stateManager: dropped state broadcast, renderer still busy")
		}
	}
}
