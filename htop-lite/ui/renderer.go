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

// ── ANSI escape helpers ────────────────────────────────────────────────────────

const (
	esc   = "\x1b["
	reset = "\x1b[0m"

	// Cursor / screen control
	clearScreen    = "\x1b[2J"
	cursorHome     = "\x1b[H"
	hideCursor     = "\x1b[?25l"
	showCursor     = "\x1b[?25h"
	cursorTo       = "\x1b[%d;%dH" // row, col (1-indexed)
	clearLine      = "\x1b[2K"

	// Text styles
	bold      = "\x1b[1m"
	dim       = "\x1b[2m"
	underline = "\x1b[4m"
	reverse   = "\x1b[7m"

	// Foreground colours (256-colour)
	fgDefault  = "\x1b[39m"
	fgBlack    = "\x1b[30m"
	fgRed      = "\x1b[91m" // bright red
	fgGreen    = "\x1b[92m" // bright green
	fgYellow   = "\x1b[93m" // bright yellow
	fgBlue     = "\x1b[94m" // bright blue
	fgMagenta  = "\x1b[95m" // bright magenta
	fgCyan     = "\x1b[96m" // bright cyan
	fgWhite    = "\x1b[97m" // bright white
	fgGray     = "\x1b[90m" // dark gray

	// Background colours
	bgDefault  = "\x1b[49m"
	bgDarkGray = "\x1b[100m"
	bgBlue     = "\x1b[44m"
	bgCyan     = "\x1b[46m"
)

// fg256 returns an ANSI escape for a 256-colour foreground.
func fg256(n int) string { return fmt.Sprintf("\x1b[38;5;%dm", n) }

// bg256 returns an ANSI escape for a 256-colour background.
func bg256(n int) string { return fmt.Sprintf("\x1b[48;5;%dm", n) }

// ── Bar chart helpers ──────────────────────────────────────────────────────────

// barChars are the Unicode block elements used to draw smooth bar charts.
// From empty to full: space, ▏▎▍▌▋▊▉█
var barChars = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'}

// renderBar draws a filled bar of width `width` representing `pct` (0–100).
// The bar uses a gradient colour: green → yellow → red as usage climbs.
func renderBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	filled := pct / 100.0 * float64(width)
	fullBlocks := int(filled)
	remainder := filled - float64(fullBlocks)
	partialIdx := int(remainder * float64(len(barChars)-1))

	color := barColor(pct)

	var sb strings.Builder
	sb.WriteString(color)
	for i := 0; i < fullBlocks && i < width; i++ {
		sb.WriteRune('█')
	}
	if fullBlocks < width && partialIdx > 0 {
		sb.WriteRune(barChars[partialIdx])
		fullBlocks++
	}
	// Dim empty space
	sb.WriteString(dim + fgGray)
	for i := fullBlocks; i < width; i++ {
		sb.WriteRune('░')
	}
	sb.WriteString(reset)
	return sb.String()
}

// barColor returns a colour escape that transitions green→yellow→red.
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

// ── Layout constants ───────────────────────────────────────────────────────────

const (
	minTermWidth  = 60
	minTermHeight = 15
)

// ── Renderer ──────────────────────────────────────────────────────────────────

// Renderer owns all writes to stdout. It receives SystemState snapshots
// and redraws the full terminal UI on each frame. It is the only goroutine
// that calls fmt.Print / os.Stdout.Write.
type Renderer struct {
	inputHandler *InputHandler // so we can call SetVisibleRows each frame
	buf          strings.Builder
	width        int
	height       int
}

// NewRenderer constructs a Renderer. Call Init() before Run().
func NewRenderer() *Renderer {
	return &Renderer{}
}

// SetInputHandler links the renderer to the input handler so it can report
// the current visible row count after each draw.
func (r *Renderer) SetInputHandler(h *InputHandler) {
	r.inputHandler = h
}

// Init hides the cursor and performs an initial clear. Must be paired with
// a deferred call to Cleanup().
func (r *Renderer) Init() error {
	r.updateSize()
	fmt.Print(hideCursor + clearScreen + cursorHome)
	log.Println("renderer: initialised")
	return nil
}

// Cleanup restores the terminal to a usable state. Always call this on exit.
func (r *Renderer) Cleanup() {
	fmt.Print(showCursor + reset + "\n")
	log.Println("renderer: cleaned up")
}

// Run is the renderer's main loop. It blocks on stateCh and redraws the
// terminal whenever a new SystemState arrives.
func (r *Renderer) Run(ctx context.Context, stateCh <-chan state.SystemState) {
	log.Println("renderer: started")

	for {
		select {
		case <-ctx.Done():
			log.Println("renderer: context cancelled, exiting")
			return

		case s := <-stateCh:
			r.updateSize()
			r.draw(s)
		}
	}
}

// updateSize queries the current terminal dimensions.
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

// ── Drawing ────────────────────────────────────────────────────────────────────

// draw builds the entire frame into r.buf and flushes it in a single write.
// Single-write rendering eliminates visible tearing on fast terminals.
//
// Rather than using a hardcoded headerRows constant (which breaks when the
// number of CPU cores varies), we draw the header into a temporary buffer
// first, count the exact number of \n characters written, then use that
// count to compute how many rows remain for the process list.
func (r *Renderer) draw(s state.SystemState) {
	r.buf.Reset()
	r.buf.WriteString(cursorHome) // jump to top-left without clearing (avoids flicker)

	// ── Phase 1: draw header into r.buf, counting rows as we go ──────────
	rowsBefore := r.rowsWritten()
	r.drawTitleBar(s)
	r.drawCPUSection(s)
	r.drawMemSection(s)
	r.drawNetSection(s)
	r.drawDivider()
	r.drawProcessHeader(s)
	r.drawDivider()
	headerRowsUsed := r.rowsWritten() - rowsBefore

	// ── Phase 2: compute how many rows the process list can occupy ────────
	// footerRows = 2: one divider + one keybind bar
	const footerRows = 2
	visibleRows := r.height - headerRowsUsed - footerRows
	if visibleRows < 1 {
		visibleRows = 1
	}

	// ── Phase 3: draw process list and footer ─────────────────────────────
	r.drawProcessList(s, visibleRows)
	if r.inputHandler != nil {
		r.inputHandler.SetVisibleRows(visibleRows)
	}
	r.drawFooter(s)

	// Single atomic write to stdout.
	os.Stdout.WriteString(r.buf.String())
}

// rowsWritten counts the number of \n characters written to r.buf so far.
// Because writeLine always emits exactly one \r\n per call, this equals
// the number of terminal rows consumed since the last r.buf.Reset().
func (r *Renderer) rowsWritten() int {
	count := 0
	for _, b := range r.buf.String() {
		if b == '\n' {
			count++
		}
	}
	return count
}

// writeLine appends a line to r.buf, truncating it to terminal width to
// prevent wrapping, then terminating with \r\n.
//
// \r\n is required in raw terminal mode: \n moves the cursor down but does
// NOT return it to column 1. Without the \r, each successive line starts
// one character further right than the last, causing the diagonal drift
// that produces the "going over several lines" display bug.
//
// Truncation is done on visible characters only — ANSI escape codes are
// skipped so colour sequences are never counted as screen columns, and a
// closing reset is always appended so colour never bleeds into the next line.
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
			inEscape = true
			out.WriteRune(ch)
		case inEscape:
			out.WriteRune(ch)
			if ch == 'm' {
				inEscape = false
			}
		default:
			if visible >= maxVisible {
				// Reached the terminal edge — stop adding visible chars.
				break
			}
			out.WriteRune(ch)
			visible++
		}
	}

	// Always reset at end of line so colours don't bleed across lines.
	out.WriteString(reset)
	r.buf.WriteString(out.String())
	r.buf.WriteString("\r\n")
}

// drawTitleBar renders the top bar with the program name and current time.
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

// drawCPUSection renders the overall CPU bar plus one bar per core.
func (r *Renderer) drawCPUSection(s state.SystemState) {
	barW := r.barWidth()

	// Overall CPU
	label := bold + fgCyan + "  CPU" + reset
	pct := s.CPU.UsagePercent
	r.writeLine(fmt.Sprintf("%s [%s] %s",
		label,
		renderBar(pct, barW),
		bold+barColor(pct)+fmt.Sprintf("%5.1f%%", pct)+reset,
	))

	// Per-core bars (up to 8 shown; collapse if terminal is narrow)
	cores := s.CPU.CoreUsage
	if len(cores) > 8 {
		cores = cores[:8]
	}
	cols := 2
	if r.width < 100 {
		cols = 1
	}
	coreBarW := (barW - 4) / cols

	for i := 0; i < len(cores); i += cols {
		var line strings.Builder
		line.WriteString("       ") // indent to align under CPU bar
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

// drawMemSection renders the memory usage bar (used / total).
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

// drawNetSection renders upload/download rates.
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

// drawDivider draws a full-width horizontal rule.
func (r *Renderer) drawDivider() {
	r.writeLine(dim + fgGray + strings.Repeat("─", r.width))
}

// drawProcessHeader renders the column headers, highlighting the active
// sort column.
func (r *Renderer) drawProcessHeader(s state.SystemState) {
	sortBy := s.SortBy.HRLabel()

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

// drawProcessList renders the scrollable, filterable process table.
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
			r.writeLine(fmt.Sprintf("%s%s▶ %s  %-*s  %s  %s  %s",
				bg256(236), bold,
				fgCyan+pidStr+reset+bg256(236)+bold,
				nameW, fgWhite+nameStr+reset+bg256(236)+bold,
				barColor(p.CPUPercent)+cpuStr+reset+bg256(236)+bold,
				barColor(p.MemPercent)+memStr+reset+bg256(236)+bold,
				fgGray+memAbs+reset,
			))
		} else {
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

	// Fill remaining rows with blank lines to avoid ghost rows from the
	// previous frame when the list shrinks.
	rendered := end - start
	for i := rendered; i < visibleRows; i++ {
		r.writeLine("")
	}
}

// drawFooter renders the keybinding hint bar and optional filter prompt.
func (r *Renderer) drawFooter(s state.SystemState) {
	r.drawDivider()

	total := len(s.Processes)
	indicator := fmt.Sprintf(dim+fgGray+"%d/%d"+reset, s.Selected+1, total)

	keys := []struct{ key, desc string }{
		{"q", "quit"},
		{"↑↓/jk", "scroll"},
		{"s", "sort:" + s.SortBy.HRLabel()},
		{"/", "filter"},
		{"x", "kill"},
	}

	var kb strings.Builder
	for _, k := range keys {
		kb.WriteString(bg256(238) + bold + fgWhite + " " + k.key + " " + reset)
		kb.WriteString(dim + fgGray + " " + k.desc + "  " + reset)
	}

	// Filter prompt overrides the keybind bar when active.
	if s.FilterQuery != "" {
		kb.Reset()
		kb.WriteString(bold + fgYellow + "  filter: " + reset)
		kb.WriteString(fgWhite + s.FilterQuery + reset)
		kb.WriteString(fgGreen + "█" + reset) // blinking cursor simulation
	}

	padding := r.width - visibleLen(kb.String()) - visibleLen(indicator) - 2
	if padding < 0 {
		padding = 0
	}

	r.writeLine(kb.String() + strings.Repeat(" ", padding) + indicator)
}

// ── Layout helpers ─────────────────────────────────────────────────────────────

// barWidth returns the character width available for metric bars, leaving
// room for the label, percentage readout, and padding.
func (r *Renderer) barWidth() int {
	w := r.width - 20
	if w < 10 {
		return 10
	}
	return w
}

// nameColWidth returns the character width for the process name column.
func (r *Renderer) nameColWidth() int {
	// PID(7) + NAME + CPU%(6) + MEM%(6) + MEM(9) + spacing(~10)
	w := r.width - 7 - 6 - 6 - 9 - 14
	if w < 10 {
		return 10
	}
	return w
}

// ── String / format helpers ────────────────────────────────────────────────────

// truncate shortens s to maxLen runes, appending "…" if truncated.
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

// formatBytes formats a byte count as a human-readable string (KB/MB/GB).
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

// formatDuration formats a time.Time as an uptime string (e.g. "3d 2h 14m").
// We use the CPU snapshot timestamp as a proxy for "time since last reboot"
// only for display; real uptime would come from /proc/uptime.
func formatDuration(t time.Time) string {
	// In a real implementation, read from /proc/uptime. Here we use the
	// age of the snapshot as a stand-in for demonstration purposes.
	d := time.Since(t)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%dm %02ds", m, s)
}

// visibleLen returns the number of printable characters in s, stripping
// ANSI escape sequences. Used for padding calculations so that escape codes
// don't count toward visual width.
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
			// still inside escape sequence
		default:
			count++
		}
	}
	return count
}

// clampF constrains a float64 to [lo, hi].
func clampF(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
