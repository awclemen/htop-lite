// Package ui provides the terminal input handler and renderer for htop-lite.
//
// This file contains the Renderer. The Renderer is responsible for drawing the
// terminal user interface. It receives complete SystemState snapshots from the
// state manager and redraws the screen whenever new data arrives.
//
// In simple terms:
//   - manager.go builds the current SystemState.
//   - renderer.go receives that SystemState.
//   - renderer.go turns that state into text, colors, bars, and tables.
//   - renderer.go writes the finished frame to the terminal.
//
// The renderer should be the only part of the program that writes directly to
// stdout. This prevents multiple goroutines from printing over each other.
package ui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"context"
	"log"

	"golang.org/x/term"
	"htop-lite/state"
)

// ─────────────────────────────────────────────────────────────────────────────
// ANSI escape helpers
// ─────────────────────────────────────────────────────────────────────────────
//
// ANSI escape codes are special strings that tell the terminal to do things
// like move the cursor, clear the screen, change colors, or apply bold text.
//
// Most of these start with ESC, which is written as "\x1b" in Go strings.

const (
	// esc is the start of many ANSI escape sequences.
	esc = "\x1b["

	// reset clears text styling and returns colors back to normal.
	reset = "\x1b[0m"

	// Cursor / screen control.
	clearScreen = "\x1b[2J"   // clears the whole terminal screen
	cursorHome  = "\x1b[H"    // moves cursor to row 1, column 1
	hideCursor  = "\x1b[?25l" // hides the blinking terminal cursor
	showCursor  = "\x1b[?25h" // shows the terminal cursor again
	cursorTo    = "\x1b[%d;%dH"
	clearLine   = "\x1b[2K"

	// Text styles.
	bold      = "\x1b[1m"
	dim       = "\x1b[2m"
	underline = "\x1b[4m"
	reverse   = "\x1b[7m"

	// Foreground colors.
	fgDefault = "\x1b[39m"
	fgBlack   = "\x1b[30m"
	fgRed     = "\x1b[91m"
	fgGreen   = "\x1b[92m"
	fgYellow  = "\x1b[93m"
	fgBlue    = "\x1b[94m"
	fgMagenta = "\x1b[95m"
	fgCyan    = "\x1b[96m"
	fgWhite   = "\x1b[97m"
	fgGray    = "\x1b[90m"

	// Background colors.
	bgDefault  = "\x1b[49m"
	bgDarkGray = "\x1b[100m"
	bgBlue     = "\x1b[44m"
	bgCyan     = "\x1b[46m"
)

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   fg256
//
// Purpose:
//   Builds an ANSI escape code for a 256-color foreground color.
//
// Pre-conditions:
//   - n should be a valid 256-color terminal color number, usually 0 through
//     255.
//
// Post-conditions:
//   - Returns a string that changes the foreground/text color.
//
// Parameters and information direction:
//   - n: input; the 256-color number.
//   - returns: output; ANSI escape string.
////////////////////////////////////////////////////////////////////////////////
func fg256(n int) string {
	return fmt.Sprintf("\x1b[38;5;%dm", n)
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   bg256
//
// Purpose:
//   Builds an ANSI escape code for a 256-color background color.
//
// Pre-conditions:
//   - n should be a valid 256-color terminal color number, usually 0 through
//     255.
//
// Post-conditions:
//   - Returns a string that changes the background color.
//
// Parameters and information direction:
//   - n: input; the 256-color number.
//   - returns: output; ANSI escape string.
////////////////////////////////////////////////////////////////////////////////
func bg256(n int) string {
	return fmt.Sprintf("\x1b[48;5;%dm", n)
}

// ─────────────────────────────────────────────────────────────────────────────
// Bar chart helpers
// ─────────────────────────────────────────────────────────────────────────────

// barChars contains partial block characters used to draw smoother bars.
//
// The characters go from almost empty to completely full:
//   space, ▏, ▎, ▍, ▌, ▋, ▊, ▉, █
//
// This lets the program show more precise CPU/memory bars than only using
// full blocks.
var barChars = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   renderBar
//
// Purpose:
//   Creates a colored usage bar for percentages like CPU usage or memory usage.
//   The bar fills based on pct and uses green, yellow, or red depending on how
//   high the percentage is.
//
// Pre-conditions:
//   - pct should normally be between 0 and 100.
//   - width should be greater than 0 to draw a visible bar.
//
// Post-conditions:
//   - Returns a string containing ANSI color codes and block characters.
//   - Does not directly print anything to the terminal.
//
// Parameters and information direction:
//   - pct: input; the usage percentage to represent.
//   - width: input; how many terminal columns wide the bar should be.
//   - returns: output; formatted string containing the bar.
////////////////////////////////////////////////////////////////////////////////
func renderBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}

	// Convert the percentage into how many characters should be filled.
	filled := pct / 100.0 * float64(width)

	// fullBlocks is the number of complete █ characters.
	fullBlocks := int(filled)

	// remainder is the leftover fractional part after full blocks.
	remainder := filled - float64(fullBlocks)

	// partialIdx chooses one of the partial block characters.
	partialIdx := int(remainder * float64(len(barChars)-1))

	// Choose green/yellow/red depending on the percentage.
	color := barColor(pct)

	var sb strings.Builder

	// Start with the usage color.
	sb.WriteString(color)

	// Draw the full part of the bar.
	for i := 0; i < fullBlocks && i < width; i++ {
		sb.WriteRune('█')
	}

	// Draw a partial block if the percentage does not land exactly on a full
	// block boundary.
	if fullBlocks < width && partialIdx > 0 {
		sb.WriteRune(barChars[partialIdx])
		fullBlocks++
	}

	// Draw the empty part of the bar in dim gray.
	sb.WriteString(dim + fgGray)
	for i := fullBlocks; i < width; i++ {
		sb.WriteRune('░')
	}

	// Reset color/style so the rest of the line is not affected.
	sb.WriteString(reset)

	return sb.String()
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   barColor
//
// Purpose:
//   Chooses a color based on a usage percentage. Low usage is green, medium
//   usage is yellow, and high usage is red.
//
// Pre-conditions:
//   - pct should represent a usage percentage.
//
// Post-conditions:
//   - Returns an ANSI foreground color string.
//
// Parameters and information direction:
//   - pct: input; usage percentage.
//   - returns: output; ANSI color string.
////////////////////////////////////////////////////////////////////////////////
func barColor(pct float64) string {
	switch {
	case pct < 50:
		return fgGreen
	case pct < 80:
		return fgYellow
	default:
		return fgRed
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Layout constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	// minTermWidth is the smallest terminal width the renderer trusts.
	// If the real terminal is smaller or cannot be detected, the renderer uses
	// a safe default size instead.
	minTermWidth = 60

	// minTermHeight is the smallest terminal height the renderer trusts.
	minTermHeight = 15
)

// ─────────────────────────────────────────────────────────────────────────────
// Renderer type
// ─────────────────────────────────────────────────────────────────────────────

// Renderer owns all terminal drawing.
//
// It receives SystemState snapshots from the state manager and redraws the
// entire terminal UI for each new snapshot.
//
// Fields:
//   - inputHandler lets the renderer tell the input handler how many process
//     rows are visible.
//   - buf stores the entire frame before it is written to stdout.
//   - width and height store the current terminal size.
type Renderer struct {
	inputHandler *InputHandler
	buf          strings.Builder
	width        int
	height       int
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   NewRenderer
//
// Purpose:
//   Creates and returns a new Renderer.
//
// Pre-conditions:
//   - None.
//
// Post-conditions:
//   - Returns a pointer to a new Renderer.
//   - Init should be called before Run.
//
// Parameters and information direction:
//   - returns: output; pointer to a Renderer.
////////////////////////////////////////////////////////////////////////////////
func NewRenderer() *Renderer {
	return &Renderer{}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   SetInputHandler
//
// Purpose:
//   Links the renderer to the input handler. This lets the renderer tell the
//   input handler how many process rows are visible after each draw.
//
// Pre-conditions:
//   - h should point to the active InputHandler.
//
// Post-conditions:
//   - r.inputHandler stores the provided handler pointer.
//
// Parameters and information direction:
//   - h: input; pointer to the input handler.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) SetInputHandler(h *InputHandler) {
	r.inputHandler = h
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   Init
//
// Purpose:
//   Prepares the terminal for the renderer. It checks terminal size, hides the
//   cursor, clears the screen, and moves the cursor to the top-left corner.
//
// Pre-conditions:
//   - The program should be running in a real terminal.
//   - stdout should be available.
//
// Post-conditions:
//   - Terminal size is stored in r.width and r.height.
//   - Cursor is hidden.
//   - Screen is cleared.
//   - A startup message is written to the log.
//
// Parameters and information direction:
//   - returns: output; nil unless future error logic is added.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) Init() error {
	r.updateSize()
	fmt.Print(hideCursor + clearScreen + cursorHome)
	log.Println("renderer: initialised")
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   Cleanup
//
// Purpose:
//   Restores the terminal after the renderer exits. This shows the cursor again
//   and resets text colors/styles.
//
// Pre-conditions:
//   - Should be called when the renderer is done.
//   - Usually called with defer after Init succeeds.
//
// Post-conditions:
//   - Cursor is visible again.
//   - Text styling is reset.
//   - A cleanup message is written to the log.
//
// Parameters and information direction:
//   - No parameters.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) Cleanup() {
	fmt.Print(showCursor + reset + "\n")
	log.Println("renderer: cleaned up")
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   Run
//
// Purpose:
//   Runs the renderer's main loop. It waits for SystemState snapshots from the
//   state manager and redraws the terminal whenever a new state arrives.
//
// Pre-conditions:
//   - ctx must be a valid context.
//   - stateCh must be a valid channel receiving SystemState values.
//   - Init should be called before Run.
//
// Post-conditions:
//   - The terminal is redrawn whenever new state arrives.
//   - The method exits when ctx is cancelled.
//
// Parameters and information direction:
//   - ctx: input; controls shutdown.
//   - stateCh: input; receives SystemState snapshots from the state manager.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) Run(ctx context.Context, stateCh <-chan state.SystemState) {
	log.Println("renderer: started")

	for {
		select {
		case <-ctx.Done():
			log.Println("renderer: context cancelled, exiting")
			return

		case s := <-stateCh:
			// Terminal size can change while the program runs, so update before
			// each draw.
			r.updateSize()

			// Draw the newest state.
			r.draw(s)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   updateSize
//
// Purpose:
//   Reads the current terminal dimensions and stores them in the renderer.
//   If the terminal size cannot be read or is too small, this method falls back
//   to a safe default of 80x24.
//
// Pre-conditions:
//   - stdout should be connected to a terminal for accurate sizing.
//
// Post-conditions:
//   - r.width and r.height are updated.
//   - If terminal size is unavailable, r.width becomes 80 and r.height becomes
//     24.
//
// Parameters and information direction:
//   - No parameters.
//   - Updates r.width and r.height.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) updateSize() {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < minTermWidth || h < minTermHeight {
		r.width = 80
		r.height = 24
		return
	}

	r.width = w
	r.height = h
}

// ─────────────────────────────────────────────────────────────────────────────
// Drawing methods
// ─────────────────────────────────────────────────────────────────────────────

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   draw
//
// Purpose:
//   Builds one complete UI frame and writes it to stdout. This method draws the
//   title bar, CPU section, memory section, network section, process table, and
//   footer.
//
// Pre-conditions:
//   - s should contain the current SystemState.
//   - r.width and r.height should already be updated.
//   - r.buf should be available for building the frame.
//
// Post-conditions:
//   - A complete terminal frame is written to stdout.
//   - The input handler may be updated with the number of visible process rows.
//
// Parameters and information direction:
//   - s: input; complete current state to display.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) draw(s state.SystemState) {
	// Clear the frame buffer so we can build a new frame from scratch.
	r.buf.Reset()

	// Move the cursor to the top-left corner.
	// This redraws over the old frame without clearing the whole screen every
	// time, which helps avoid flicker.
	r.buf.WriteString(cursorHome)

	// Phase 1:
	// Draw the header area and count how many rows it used.
	rowsBefore := r.rowsWritten()
	r.drawTitleBar(s)
	r.drawCPUSection(s)
	r.drawMemSection(s)
	r.drawNetSection(s)
	r.drawDivider()
	r.drawProcessHeader(s)
	r.drawDivider()
	headerRowsUsed := r.rowsWritten() - rowsBefore

	// Phase 2:
	// Figure out how much vertical space is left for the process list.
	// The footer uses two rows: one divider and one keybind/filter line.
	const footerRows = 2
	visibleRows := r.height - headerRowsUsed - footerRows
	if visibleRows < 1 {
		visibleRows = 1
	}

	// Phase 3:
	// Draw the process list and footer.
	r.drawProcessList(s, visibleRows)

	// Tell the input handler how many rows are visible so scrolling works with
	// the current terminal size.
	if r.inputHandler != nil {
		r.inputHandler.SetVisibleRows(visibleRows)
	}

	r.drawFooter(s)

	// Write the whole frame in one operation.
	// This reduces tearing/flickering compared to printing line by line.
	os.Stdout.WriteString(r.buf.String())
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   rowsWritten
//
// Purpose:
//   Counts how many lines have been written into the frame buffer so far.
//
// Pre-conditions:
//   - r.buf may contain part of the current frame.
//   - writeLine should be the main way rows are added.
//
// Post-conditions:
//   - Returns the number of newline characters currently in r.buf.
//
// Parameters and information direction:
//   - returns: output; number of rows written so far.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) rowsWritten() int {
	count := 0
	for _, b := range r.buf.String() {
		if b == '\n' {
			count++
		}
	}
	return count
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   writeLine
//
// Purpose:
//   Adds one line to the frame buffer. This method prevents lines from wrapping
//   past the terminal width and makes sure ANSI color codes do not count as
//   visible characters.
//
// Pre-conditions:
//   - r.width should contain the current terminal width.
//   - s may contain ANSI escape codes.
//
// Post-conditions:
//   - A safely truncated line is appended to r.buf.
//   - The line ends with "\r\n".
//   - Text styling is reset at the end of the line.
//
// Parameters and information direction:
//   - s: input; line to append to the frame buffer.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) writeLine(s string) {
	maxVisible := r.width
	if maxVisible <= 0 {
		r.buf.WriteString("\r\n")
		return
	}

	visible := 0
	inEscape := false
	var out strings.Builder

	for _, ch := range s {
		switch {
		case ch == '\x1b':
			// Start of an ANSI escape sequence.
			inEscape = true
			out.WriteRune(ch)

		case inEscape:
			// Copy ANSI escape characters without counting them as visible
			// terminal columns.
			out.WriteRune(ch)
			if ch == 'm' {
				inEscape = false
			}

		default:
			// Stop adding visible characters once the line reaches the terminal
			// edge. This prevents unwanted wrapping.
			if visible >= maxVisible {
				break
			}
			out.WriteRune(ch)
			visible++
		}
	}

	// Reset styling at the end of every line so colors do not bleed into the
	// next line.
	out.WriteString(reset)

	// In raw terminal mode, "\n" moves down but does not always return to the
	// beginning of the line. "\r\n" keeps the next line aligned.
	r.buf.WriteString(out.String())
	r.buf.WriteString("\r\n")
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   drawTitleBar
//
// Purpose:
//   Draws the top title bar containing the program name, the display uptime
//   string, and the current time.
//
// Pre-conditions:
//   - s should contain a CPU timestamp.
//   - r.width should contain the current terminal width.
//
// Post-conditions:
//   - One title-bar line is added to the frame buffer.
//
// Parameters and information direction:
//   - s: input; current SystemState.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) drawTitleBar(s state.SystemState) {
	now := time.Now().Format("15:04:05")
	title := bold + bg256(24) + fgWhite + " htop-lite " + reset
	timeStr := dim + fgCyan + now + reset
	uptime := fmt.Sprintf("  up %s", formatDuration(s.CPU.Timestamp))

	padding := r.width - visibleLen(" htop-lite ") - visibleLen(now) - visibleLen(uptime) - 1
	if padding < 0 {
		padding = 0
	}

	line := title + strings.Repeat(" ", padding) + dim + uptime + reset + "  " + timeStr
	r.writeLine(line)
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   drawCPUSection
//
// Purpose:
//   Draws the overall CPU usage bar and individual CPU core usage bars.
//
// Pre-conditions:
//   - s.CPU should contain CPU usage data.
//   - r.width should contain the current terminal width.
//
// Post-conditions:
//   - CPU usage lines are added to the frame buffer.
//   - At most 8 individual cores are displayed.
//
// Parameters and information direction:
//   - s: input; current SystemState.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) drawCPUSection(s state.SystemState) {
	barW := r.barWidth()

	// Draw overall CPU usage.
	label := bold + fgCyan + "  CPU" + reset
	pct := s.CPU.UsagePercent
	r.writeLine(fmt.Sprintf("%s [%s] %s",
		label,
		renderBar(pct, barW),
		bold+barColor(pct)+fmt.Sprintf("%5.1f%%", pct)+reset,
	))

	// Draw per-core CPU bars.
	// Limit to 8 cores so huge systems do not take up the whole terminal.
	cores := s.CPU.CoreUsage
	if len(cores) > 8 {
		cores = cores[:8]
	}

	// Use two columns on wider terminals and one column on narrower terminals.
	cols := 2
	if r.width < 100 {
		cols = 1
	}

	coreBarW := (barW - 4) / cols

	for i := 0; i < len(cores); i += cols {
		var line strings.Builder
		line.WriteString("       ")

		for c := 0; c < cols && i+c < len(cores); c++ {
			idx := i + c
			corePct := cores[idx]

			line.WriteString(fmt.Sprintf(
				fgGray+"%d"+reset+"[%s]%s  ",
				idx,
				renderBar(corePct, coreBarW),
				bold+barColor(corePct)+fmt.Sprintf("%4.1f%%", corePct)+reset,
			))
		}

		r.writeLine(line.String())
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   drawMemSection
//
// Purpose:
//   Draws the memory usage bar, memory percentage, and used/total memory text.
//
// Pre-conditions:
//   - s.Memory should contain memory data.
//   - r.width should contain the current terminal width.
//
// Post-conditions:
//   - One memory line is added to the frame buffer.
//
// Parameters and information direction:
//   - s: input; current SystemState.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) drawMemSection(s state.SystemState) {
	barW := r.barWidth()
	m := s.Memory
	pct := m.UsedPercent

	usedStr := formatBytes(m.Used)
	totalStr := formatBytes(m.Total)

	label := bold + fgMagenta + "  MEM" + reset
	info := fmt.Sprintf("%s/%s", usedStr, totalStr)

	r.writeLine(fmt.Sprintf("%s [%s] %s %s",
		label,
		renderBar(pct, barW),
		bold+barColor(pct)+fmt.Sprintf("%5.1f%%", pct)+reset,
		dim+fgGray+info+reset,
	))
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   drawNetSection
//
// Purpose:
//   Draws the network upload and download rates.
//
// Pre-conditions:
//   - s.Network should contain network rate data.
//
// Post-conditions:
//   - One network line is added to the frame buffer.
//
// Parameters and information direction:
//   - s: input; current SystemState.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) drawNetSection(s state.SystemState) {
	n := s.Network

	r.writeLine(fmt.Sprintf(
		"  "+bold+fgBlue+"NET"+reset+"  "+
			fgGreen+"↑ %-10s"+reset+
			fgYellow+"↓ %-10s"+reset,
		formatBytes(n.BytesSentPerSec)+"/s",
		formatBytes(n.BytesRecvPerSec)+"/s",
	))
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   drawDivider
//
// Purpose:
//   Draws a horizontal divider line across the width of the terminal.
//
// Pre-conditions:
//   - r.width should contain the current terminal width.
//
// Post-conditions:
//   - One divider line is added to the frame buffer.
//
// Parameters and information direction:
//   - No parameters.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) drawDivider() {
	r.writeLine(dim + fgGray + strings.Repeat("─", r.width))
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   drawProcessHeader
//
// Purpose:
//   Draws the process table column headers. The currently active sort column is
//   highlighted with cyan underlined text.
//
// Pre-conditions:
//   - s.SortBy should contain the current sort mode.
//   - r.width should contain the current terminal width.
//
// Post-conditions:
//   - One process-header line is added to the frame buffer.
//
// Parameters and information direction:
//   - s: input; current SystemState.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) drawProcessHeader(s state.SystemState) {
	sortBy := s.SortBy.HRLabel()

	// col formats one column header. If the column is the active sort column,
	// it gets highlighted.
	col := func(name string, w int) string {
		if name == sortBy {
			return bold + fgCyan + underline + fmt.Sprintf("%-*s", w, name) + reset
		}
		return bold + fgGray + fmt.Sprintf("%-*s", w, name) + reset
	}

	r.writeLine(fmt.Sprintf("  %s  %s  %s  %s  %s",
		col("PID", 7),
		col("NAME", r.nameColWidth()),
		col("CPU%", 6),
		col("MEM%", 6),
		col("MEM", 9),
	))
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   drawProcessList
//
// Purpose:
//   Draws the visible part of the process table. The full process list may be
//   longer than the terminal, so this method only draws the slice of processes
//   starting at ScrollOffset and ending after visibleRows.
//
// Pre-conditions:
//   - s.Processes should contain the filtered and sorted process list.
//   - s.ScrollOffset should be a valid starting index.
//   - s.Selected should point to the selected process if one exists.
//   - visibleRows should be the number of rows available for the process list.
//
// Post-conditions:
//   - Process rows are added to the frame buffer.
//   - The selected process row is highlighted.
//   - Blank rows are added if needed to erase leftover rows from older frames.
//
// Parameters and information direction:
//   - s: input; current SystemState.
//   - visibleRows: input; number of process rows to draw.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) drawProcessList(s state.SystemState, visibleRows int) {
	procs := s.Processes
	start := s.ScrollOffset
	end := start + visibleRows

	if end > len(procs) {
		end = len(procs)
	}

	nameW := r.nameColWidth()

	for i := start; i < end; i++ {
		p := procs[i]
		selected := i == s.Selected

		pidStr := fmt.Sprintf("%7d", p.PID)
		nameStr := truncate(p.Name, nameW)
		cpuStr := fmt.Sprintf("%5.1f%%", p.CPUPercent)
		memStr := fmt.Sprintf("%5.1f%%", p.MemPercent)
		memAbs := formatBytes(p.MemBytes)

		if selected {
			// Highlight the selected process row with a background color and
			// pointer arrow.
			r.writeLine(fmt.Sprintf("%s%s▶ %s  %-*s  %s  %s  %s",
				bg256(236), bold,
				fgCyan+pidStr+reset+bg256(236)+bold,
				nameW, fgWhite+nameStr+reset+bg256(236)+bold,
				barColor(p.CPUPercent)+cpuStr+reset+bg256(236)+bold,
				barColor(p.MemPercent)+memStr+reset+bg256(236)+bold,
				fgGray+memAbs+reset,
			))
		} else {
			// For normal rows, only highlight CPU strongly when it is high.
			cpuColor := fgDefault
			if p.CPUPercent > 50 {
				cpuColor = barColor(p.CPUPercent)
			}

			r.writeLine(fmt.Sprintf("  %s  %-*s  %s  %s  %s",
				fgGray+pidStr+reset,
				nameW, fgWhite+nameStr+reset,
				bold+cpuColor+cpuStr+reset,
				barColor(p.MemPercent)+memStr+reset,
				dim+fgGray+memAbs+reset,
			))
		}
	}

	// If fewer processes were drawn than visibleRows, fill the rest of the
	// space with blank lines. This prevents old process rows from staying on
	// screen after filtering or scrolling.
	rendered := end - start
	for i := rendered; i < visibleRows; i++ {
		r.writeLine("")
	}
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   drawFooter
//
// Purpose:
//   Draws the bottom help/status line. Normally it shows keybind hints. If a
//   filter is active, it shows the current filter text instead.
//
// Pre-conditions:
//   - s should contain the current sort mode, filter query, selected index, and
//     process list.
//   - r.width should contain the current terminal width.
//
// Post-conditions:
//   - A divider and footer line are added to the frame buffer.
//
// Parameters and information direction:
//   - s: input; current SystemState.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) drawFooter(s state.SystemState) {
	r.drawDivider()

	total := len(s.Processes)
	indicator := fmt.Sprintf(dim+fgGray+"%d/%d"+reset, s.Selected+1, total)

	keys := []struct {
		key  string
		desc string
	}{
		{"q", "quit"},
		{"↑↓/jk", "scroll"},
		{"s", "sort:" + s.SortBy.HRLabel()},
		{"/", "filter"},
		{"x", "kill"},
	}

	var kb strings.Builder

	// Build the keybind bar.
	for _, k := range keys {
		kb.WriteString(bg256(238) + bold + fgWhite + " " + k.key + " " + reset)
		kb.WriteString(dim + fgGray + " " + k.desc + "  " + reset)
	}

	// If filtering is active, show the filter prompt instead of the normal
	// keybind list.
	if s.FilterQuery != "" {
		kb.Reset()
		kb.WriteString(bold + fgYellow + "  filter: " + reset)
		kb.WriteString(fgWhite + s.FilterQuery + reset)
		kb.WriteString(fgGreen + "█" + reset)
	}

	padding := r.width - visibleLen(kb.String()) - visibleLen(indicator) - 2
	if padding < 0 {
		padding = 0
	}

	r.writeLine(kb.String() + strings.Repeat(" ", padding) + indicator)
}

// ─────────────────────────────────────────────────────────────────────────────
// Layout helpers
// ─────────────────────────────────────────────────────────────────────────────

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   barWidth
//
// Purpose:
//   Calculates how wide CPU and memory bars should be based on the current
//   terminal width.
//
// Pre-conditions:
//   - r.width should contain the current terminal width.
//
// Post-conditions:
//   - Returns at least 10 so bars do not become unusably small.
//
// Parameters and information direction:
//   - returns: output; width for usage bars.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) barWidth() int {
	w := r.width - 20
	if w < 10 {
		return 10
	}
	return w
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   nameColWidth
//
// Purpose:
//   Calculates the width of the process name column. The name column gets
//   whatever space is left after accounting for PID, CPU, memory, and spacing.
//
// Pre-conditions:
//   - r.width should contain the current terminal width.
//
// Post-conditions:
//   - Returns at least 10 so process names always have some space.
//
// Parameters and information direction:
//   - returns: output; width of the NAME column.
////////////////////////////////////////////////////////////////////////////////
func (r *Renderer) nameColWidth() int {
	// PID(7) + NAME + CPU%(6) + MEM%(6) + MEM(9) + spacing(~14)
	w := r.width - 7 - 6 - 6 - 9 - 14
	if w < 10 {
		return 10
	}
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// String / format helpers
// ─────────────────────────────────────────────────────────────────────────────

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   truncate
//
// Purpose:
//   Shortens a string so it fits inside a fixed-width column. If the string is
//   too long, it ends with an ellipsis.
//
// Pre-conditions:
//   - s may be any string.
//   - maxLen should be the maximum number of visible runes allowed.
//
// Post-conditions:
//   - Returns s unchanged if it already fits.
//   - Returns a shortened string ending in "…" if it is too long.
//
// Parameters and information direction:
//   - s: input; string to shorten.
//   - maxLen: input; maximum allowed rune length.
//   - returns: output; original or shortened string.
////////////////////////////////////////////////////////////////////////////////
func truncate(s string, maxLen int) string {
	runes := []rune(s)

	if len(runes) <= maxLen {
		return s
	}

	if maxLen <= 1 {
		return "…"
	}

	return string(runes[:maxLen-1]) + "…"
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   formatBytes
//
// Purpose:
//   Converts a byte count into a shorter human-readable string using units like
//   K, M, G, or T.
//
// Pre-conditions:
//   - b should be a byte count.
//
// Post-conditions:
//   - Returns a formatted string such as "512B", "1.5K", "20.3M", or "4.0G".
//
// Parameters and information direction:
//   - b: input; number of bytes.
//   - returns: output; human-readable byte string.
////////////////////////////////////////////////////////////////////////////////
func formatBytes(b uint64) string {
	const unit = 1024

	if b < unit {
		return fmt.Sprintf("%dB", b)
	}

	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f%s", float64(b)/float64(div), []string{"K", "M", "G", "T"}[exp])
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   formatDuration
//
// Purpose:
//   Formats a time value as a short duration string. In this program, it is
//   used for the "up" display in the title bar.
//
// Pre-conditions:
//   - t should be a valid time.Time.
//   - If t is zero, the result may look very large or unusual.
//
// Post-conditions:
//   - Returns a duration string like "4m 03s" or "2h 15m".
//
// Parameters and information direction:
//   - t: input; time to compare against the current time.
//   - returns: output; formatted duration string.
////////////////////////////////////////////////////////////////////////////////
func formatDuration(t time.Time) string {
	// This uses the age of the CPU snapshot as a display value.
	// A more exact system uptime would come from something like /proc/uptime.
	d := time.Since(t)

	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}

	return fmt.Sprintf("%dm %02ds", m, s)
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   visibleLen
//
// Purpose:
//   Counts how many printable characters are in a string while ignoring ANSI
//   escape codes. This is needed because color codes affect terminal styling
//   but do not take up visible screen columns.
//
// Pre-conditions:
//   - s may contain normal text and ANSI escape sequences.
//
// Post-conditions:
//   - Returns the visible length of s.
//
// Parameters and information direction:
//   - s: input; string to measure.
//   - returns: output; visible character count.
////////////////////////////////////////////////////////////////////////////////
func visibleLen(s string) int {
	inEscape := false
	count := 0

	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true

		case inEscape && r == 'm':
			inEscape = false

		case inEscape:
			// Still inside an escape sequence, so do not count this rune.

		default:
			count++
		}
	}

	return count
}

////////////////////////////////////////////////////////////////////////////////
// Method name:
//   clampF
//
// Purpose:
//   Restricts a float64 value so it stays within a given range.
//
// Pre-conditions:
//   - lo should be less than or equal to hi.
//
// Post-conditions:
//   - Returns lo if v is too small.
//   - Returns hi if v is too large.
//   - Returns v if v is already inside the range.
//
// Parameters and information direction:
//   - v: input; value to clamp.
//   - lo: input; lower bound.
//   - hi: input; upper bound.
//   - returns: output; clamped value.
////////////////////////////////////////////////////////////////////////////////
func clampF(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
