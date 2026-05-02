package state
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
// Description: manager - combines all data into single struct and sends to UI
//
// Language: Go
// Ex. Packages: context, log, os, os/signal, sync, syscall, time
//
// Deficiencies:
////////////////////////////////////////////////////////////////////////////////

import (
	"context"
	"log"
	"sort"
	"strings"

	"htop-lite/collector"
	"htop-lite/events"
)

// SortField enumerates the columns by which the process list can be sorted.
type SortField int

const (
	SortByCPU  SortField = iota // default — most active processes first
	SortByMem
	SortByPID
	SortByName
)

////////////////////////////////////////////////////////////////////////////////
// Method name: HRLabel
// Purpose: returns a human-readable label for the renderer
// Pre- and Post- conditions:
// Parameters and information direction:
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
		return "CPU%"
	}
}

type SystemState struct {
	CPU       collector.CPUSnapshot
	Memory    collector.MemSnapshot
	Processes []collector.ProcessInfo // sorted + filtered, ready to display
	Network   collector.NetworkSnapshot

	SortBy      SortField
	FilterQuery string
	ScrollOffset int // index of the first visible process row
	Selected     int // index within the visible (filtered) list
}

type Manager struct {
	current SystemState
	rawProcesses []collector.ProcessInfo
}

////////////////////////////////////////////////////////////////////////////////
// Method name: NewManager 
// Purpose: 
// Pre- and Post- conditions:
// Parameters and information direction:
////////////////////////////////////////////////////////////////////////////////
func NewManager() *Manager {
	return &Manager{
		current: SystemState{
			SortBy: SortByCPU,
		},
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name: Run
// Purpose: 
// Pre- and Post- conditions:
// Parameters and information direction:
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
			log.Println("stateManager: context cancelled, exiting")
			return

		case snap := <-cpuCh:
			m.current.CPU = snap
			m.broadcast(stateCh)

		case snap := <-memCh:
			m.current.Memory = snap
			m.broadcast(stateCh)

		case procs := <-procCh:
			m.rawProcesses = procs
			m.applyFilter()
			m.applySort()
			m.clampScroll()
			m.broadcast(stateCh)

		case snap := <-netCh:
			m.current.Network = snap
			m.broadcast(stateCh)

		case event := <-inputCh:
			m.handleInput(event, ctx)
			m.broadcast(stateCh)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name: handleInput
// Purpose: 
// Pre- and Post- conditions:
// Parameters and information direction:
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) handleInput(event events.InputEvent, ctx context.Context) {
	switch event.Type {

	case events.EventScrollUp:
		if m.current.Selected > 0 {
			m.current.Selected--
		}
		if m.current.Selected < m.current.ScrollOffset {
			m.current.ScrollOffset--
		}

	case events.EventScrollDown:
		maxIdx := len(m.current.Processes) - 1
		if m.current.Selected < maxIdx {
			m.current.Selected++
		}
		visibleRows := event.Payload.(int)
		if m.current.Selected >= m.current.ScrollOffset+visibleRows {
			m.current.ScrollOffset++
		}

	case events.EventCycleSort:
		m.current.SortBy = SortField((int(m.current.SortBy) + 1) % 4)
		m.current.Selected = 0
		m.current.ScrollOffset = 0
		m.applySort()

	case events.EventFilter:
		query, ok := event.Payload.(string)
		if !ok {
			query = ""
		}
		m.current.FilterQuery = query
		m.current.Selected = 0
		m.current.ScrollOffset = 0
		m.applyFilter()
		m.applySort()
		m.clampScroll()

	case events.EventKill:
		m.killSelected()

	case events.EventQuit:
		log.Println("stateManager: quit event received")
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name: applyFilter
// Purpose: 
// Pre- and Post- conditions:
// Parameters and information direction:
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(m.current.FilterQuery))
	if query == "" {
		// No filter — copy the full list.
		m.current.Processes = make([]collector.ProcessInfo, len(m.rawProcesses))
		copy(m.current.Processes, m.rawProcesses)
		return
	}

	filtered := m.current.Processes[:0] 
	for _, p := range m.rawProcesses {
		if strings.Contains(strings.ToLower(p.Name), query) {
			filtered = append(filtered, p)
		}
	}
	m.current.Processes = filtered
}

////////////////////////////////////////////////////////////////////////////////
// Method name: applySort
// Purpose: 
// Pre- and Post- conditions:
// Parameters and information direction:
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) applySort() {
	procs := m.current.Processes
	switch m.current.SortBy {
	case SortByCPU:
		sort.Slice(procs, func(i, j int) bool {
			return procs[i].CPUPercent > procs[j].CPUPercent
		})
	case SortByMem:
		sort.Slice(procs, func(i, j int) bool {
			return procs[i].MemPercent > procs[j].MemPercent
		})
	case SortByPID:
		sort.Slice(procs, func(i, j int) bool {
			return procs[i].PID < procs[j].PID
		})
	case SortByName:
		sort.Slice(procs, func(i, j int) bool {
			return strings.ToLower(procs[i].Name) < strings.ToLower(procs[j].Name)
		})
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name: containScroll
// Purpose: Keeps the scrolling with the range of the available processes.
// Pre- and Post- conditions:
// Parameters and information direction:
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) clampScroll() {
	total := len(m.current.Processes)
	if total == 0 {
		m.current.Selected = 0
		m.current.ScrollOffset = 0
		return
	}
	if m.current.Selected >= total {
		m.current.Selected = total - 1
	}
	if m.current.ScrollOffset > m.current.Selected {
		m.current.ScrollOffset = m.current.Selected
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name: killSelected()
// Purpose: sends SIGTERM to the currently selected process. 
// Pre- and Post- conditions:
// Parameters and information direction:
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) killSelected() {
	procs := m.current.Processes
	if len(procs) == 0 {
		return
	}
	idx := m.current.Selected
	if idx < 0 || idx >= len(procs) {
		return
	}

	target := procs[idx]
	log.Printf("stateManager: sending SIGTERM to PID %d (%s)", target.PID, target.Name)

	if err := target.Kill(); err != nil {
		log.Printf("stateManager: kill PID %d failed: %v", target.PID, err)
	} else {
		log.Printf("stateManager: killed PID %d (%s)", target.PID, target.Name)
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name: broadcast
// Purpose: pushes a copy of the current state to stateCh. 
// Pre- and Post- conditions:
// Parameters and information direction:
////////////////////////////////////////////////////////////////////////////////
func (m *Manager) broadcast(stateCh chan SystemState) {
	snap := m.current 

	select {
	case stateCh <- snap:
	default:
		// Drain stale snapshot, then send fresh one.
		select {
		case <-stateCh:
		default:
		}
		select {
		case stateCh <- snap:
		default:
			log.Println("stateManager: dropped state broadcast, renderer still busy")
		}
	}
}
