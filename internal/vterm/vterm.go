// Package vterm wraps a headless VT100/xterm emulator (internal/vt10x, an
// in-tree fork of github.com/hinshun/vt10x) behind a small, stable surface:
// feed it the raw bytes a pty produces, ask it for a rectangular snapshot of
// cells plus the cursor. The rest of ngmux never touches vt10x directly, so the
// emulator can be swapped later without churn.
//
// vt10x itself keeps no scrollback, so this package reconstructs one: before
// each write it snapshots the grid, and after the write it detects a vertical
// scroll (the old rows [k:] now equal the new rows [:-k]) and pushes the k rows
// that fell off the top into a capped history ring. Capture is suppressed while
// the child is on the alternate screen (vim, less, htop), matching tmux.
package vterm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"

	"github.com/MauricioJC3/ng_mux/internal/vt10x"
)

// ErrEmulatorPanic marks a Write error caused by a recovered panic inside the
// underlying vt10x emulator. vt10x defends against most malformed input, but
// it is a large, vendored state machine and a hostile or buggy pty payload
// can still trip an invariant it does not guard. Rather than let that panic
// climb out of Term.Write and take down whatever goroutine is pumping pty
// output (which, unrecovered, takes down the whole daemon), Write recovers,
// discards the panicking emulator, and replaces it with a fresh one of the
// same size. Callers can match this sentinel with errors.Is to distinguish
// "the emulator glitched and was reset" from an ordinary I/O error.
var ErrEmulatorPanic = errors.New("vterm: recovered panic in terminal emulator")

// newEmulator builds a vt10x terminal of the given size, wired to reply for
// device-query responses (DA, DSR, ...). It is the single constructor used by
// both New and Write's panic-recovery path, so the two can never drift.
func newEmulator(cols, rows int, reply io.Writer) vt10x.Terminal {
	opts := []vt10x.TerminalOption{vt10x.WithSize(cols, rows)}
	if reply != nil {
		opts = append(opts, vt10x.WithWriter(reply))
	}
	return vt10x.New(opts...)
}

// Attribute bits on a Cell. These mirror vt10x's internal glyph flags, which
// are not exported by that package; the values are part of its on-wire VT
// behaviour and are stable.
const (
	AttrReverse = 1 << iota
	AttrUnderline
	AttrBold
	AttrItalic
	AttrBlink
)

// ColorDefault marks a cell colour as "use the terminal default". It sits
// above the 0xRRGGBB truecolor range so it can never collide with a real one.
const ColorDefault uint32 = 1 << 24

// Cell is one character position in the emulator grid. Width is 2 for the lead
// cell of a double-width glyph, 0 for the spacer cell that follows it, and 1
// for every ordinary cell.
type Cell struct {
	Ch    rune
	FG    uint32 // palette index [0,256) or ColorDefault or (>=256) packed 0xRRGGBB
	BG    uint32
	Attr  uint16
	Width uint8
}

func blankCell() Cell { return Cell{Ch: ' ', FG: ColorDefault, BG: ColorDefault, Width: 1} }

// RuneWidth reports how many terminal columns r occupies (1 or 2). It is the
// same conservative width table the emulator uses to lay out its grid, exposed
// so callers that render their own text (the status bar) stay aligned with it.
func RuneWidth(r rune) int { return vt10x.RuneWidth(r) }

// StringWidth is the total column width of s.
func StringWidth(s string) int {
	w := 0
	for _, r := range s {
		w += RuneWidth(r)
	}
	return w
}

// Snapshot is an immutable copy of a screen region at one instant.
type Snapshot struct {
	Cols, Rows int
	Cells      []Cell // row-major, len == Cols*Rows
	CurX, CurY int
	CurVisible bool
}

// At returns the cell at (x,y). Out-of-range coordinates return a blank cell.
func (s *Snapshot) At(x, y int) Cell {
	if x < 0 || y < 0 || x >= s.Cols || y >= s.Rows {
		return blankCell()
	}
	return s.Cells[y*s.Cols+x]
}

// Term is a single emulated terminal with reconstructed scrollback.
type Term struct {
	mu   sync.Mutex
	t    vt10x.Terminal
	cols int
	rows int

	reply io.Writer // pty master: where query responses (DA, DSR) are written

	histLimit int
	history   [][]Cell // oldest first; each row is exactly cols wide at push time

	// dirty is set whenever bytes arrive or the grid is resized, and cleared by
	// Snapshot. The server's broadcaster reads it to skip repainting sessions
	// whose panes have produced nothing since the last frame. A fresh Term is
	// dirty so its first frame is always drawn.
	dirty bool
}

// New creates an emulator of the given size. reply receives the bytes the
// emulator emits in response to device queries (cursor position reports, device
// attributes, ...) and should normally be the pty master so the child program
// sees the answers.
func New(cols, rows int, reply io.Writer) *Term {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return &Term{t: newEmulator(cols, rows, reply), reply: reply, cols: cols, rows: rows, histLimit: 2000, dirty: true}
}

// Device-attribute query/response pairs. vt10x answers DSR (cursor position,
// status) itself but leaves DA a stub, so we answer it here: a shell such as
// fish sends a Primary DA request on startup and blocks for ~10s if nothing
// replies, then runs degraded. The responses mirror what tmux reports.
var deviceQueries = []struct{ query, response []byte }{
	{[]byte("\x1b[c"), []byte("\x1b[?1;2c")},      // Primary DA
	{[]byte("\x1b[0c"), []byte("\x1b[?1;2c")},     // Primary DA, explicit 0
	{[]byte("\x1b[>c"), []byte("\x1b[>84;0;0c")},  // Secondary DA ('T' = tmux)
	{[]byte("\x1b[>0c"), []byte("\x1b[>84;0;0c")}, // Secondary DA, explicit 0
}

// answerDeviceQueries scans a pty payload for terminal-identification requests
// vt10x ignores and writes the canned response for each one found. Caller holds
// t.mu.
func (t *Term) answerDeviceQueries(p []byte) {
	if t.reply == nil || bytes.IndexByte(p, 0x1b) < 0 {
		return
	}
	for _, q := range deviceQueries {
		if bytes.Contains(p, q.query) {
			_, _ = t.reply.Write(q.response)
		}
	}
}

// SetHistoryLimit caps the scrollback ring (lines). Zero disables capture.
func (t *Term) SetHistoryLimit(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n < 0 {
		n = 0
	}
	t.histLimit = n
	if len(t.history) > n {
		t.history = append(t.history[:0:0], t.history[len(t.history)-n:]...)
	}
}

// Write feeds pty output into the emulator and updates scrollback.
//
// To reconstruct scrollback, the payload is fed in newline-delimited slices so
// a scroll can be detected by diffing the grid around the write. That diff is
// only taken when a scroll is actually possible: the cursor is on (or near) the
// last row, or the slice is long enough to wrap into one. Steady output that
// does not touch the bottom of the screen costs nothing extra.
func (t *Term) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// vt10x's own Write releases its internal lock via defer, so a recovered
	// panic there never leaves it held. This defer runs before the mu.Unlock
	// above (LIFO), so by the time it fires t.mu is still ours to use: it
	// swaps in a fresh emulator, marks the pane dirty so the next Snapshot
	// repaints it (clean, if blank), and turns the panic into an error
	// instead of letting it climb out of Write and kill the caller's
	// goroutine.
	defer func() {
		if r := recover(); r != nil {
			t.t = newEmulator(t.cols, t.rows, t.reply)
			t.dirty = true
			err = fmt.Errorf("%w: %v\n%s", ErrEmulatorPanic, r, debug.Stack())
		}
	}()

	if len(p) > 0 {
		t.dirty = true
	}
	t.answerDeviceQueries(p)

	if t.histLimit <= 0 || t.altLocked() {
		return t.t.Write(p)
	}

	total := 0
	for len(p) > 0 {
		chunk := p
		if i := indexByte(p, '\n'); i >= 0 {
			chunk = p[:i+1]
		}
		p = p[len(chunk):]

		mayScroll := t.cursorYLocked() >= t.rows-1 || len(chunk) > t.cols
		if !mayScroll {
			n, err := t.t.Write(chunk)
			total += n
			if err != nil {
				return total, err
			}
			continue
		}
		before := t.gridLocked()
		n, err := t.t.Write(chunk)
		total += n
		if !t.altLocked() {
			t.captureScrollLocked(before)
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// cursorYLocked returns the emulator cursor's row. Caller holds t.mu.
func (t *Term) cursorYLocked() int {
	t.t.Lock()
	defer t.t.Unlock()
	return t.t.Cursor().Y
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// Resize changes the emulator grid size. History is kept as-is; ScrollbackView
// pads or trims old rows to the current width.
func (t *Term) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.t.Resize(cols, rows)
	t.cols, t.rows = cols, rows
	t.dirty = true
}

// Dirty reports whether bytes have arrived or the grid was resized since the
// last Snapshot. A fresh Term is dirty.
func (t *Term) Dirty() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dirty
}

// Size reports the current grid size.
func (t *Term) Size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows
}

// HistoryLen is the number of scrollback lines currently retained.
func (t *Term) HistoryLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.history)
}

// Snapshot copies the whole live screen out.
func (t *Term) Snapshot() Snapshot {
	var s Snapshot
	t.SnapshotInto(&s)
	return s
}

// SnapshotInto copies the live screen into dst, reusing dst.Cells when it is
// already large enough. Like Snapshot it clears the dirty flag. The hot render
// path uses this with a per-session scratch snapshot so a steady repaint
// allocates nothing.
func (t *Term) SnapshotInto(dst *Snapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.dirty = false

	t.t.Lock()
	defer t.t.Unlock()

	cols, rows := t.cols, t.rows
	need := cols * rows
	if cap(dst.Cells) < need {
		dst.Cells = make([]Cell, need)
	} else {
		dst.Cells = dst.Cells[:need]
	}
	dst.Cols, dst.Rows = cols, rows
	dst.CurVisible = t.t.CursorVisible()
	cur := t.t.Cursor()
	dst.CurX, dst.CurY = cur.X, cur.Y
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			dst.Cells[y*cols+x] = toCell(t.t.Cell(x, y))
		}
	}
}

// ScrollbackView returns a viewRows-tall window over history+live, starting
// offset lines above the bottom. offset 0 is the live screen; offset ==
// HistoryLen() is scrolled as far back as possible. The cursor is not shown.
func (t *Term) ScrollbackView(offset, viewRows int) Snapshot {
	if viewRows <= 0 {
		viewRows = 1
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	live := t.gridLocked()
	histLen := len(t.history)
	total := histLen + len(live)

	if offset < 0 {
		offset = 0
	}
	if offset > histLen {
		offset = histLen
	}
	end := total - offset   // exclusive index of the bottom visible row + 1
	start := end - viewRows // may be negative -> top padded with blanks

	cols := t.cols
	snap := Snapshot{Cols: cols, Rows: viewRows, Cells: make([]Cell, cols*viewRows)}
	for r := 0; r < viewRows; r++ {
		for x := 0; x < cols; x++ {
			snap.Cells[r*cols+x] = blankCell()
		}
		src := start + r
		if src < 0 || src >= total {
			continue
		}
		var row []Cell
		if src < histLen {
			row = t.history[src]
		} else {
			row = live[src-histLen]
		}
		for x := 0; x < cols && x < len(row); x++ {
			snap.Cells[r*cols+x] = row[x]
		}
	}
	return snap
}

// altLocked reports whether the child is on the alternate screen. Caller holds mu.
func (t *Term) altLocked() bool {
	return t.t.Mode()&vt10x.ModeAltScreen != 0
}

// gridLocked copies every live row as []Cell. Caller holds mu.
func (t *Term) gridLocked() [][]Cell {
	t.t.Lock()
	defer t.t.Unlock()
	rows := make([][]Cell, t.rows)
	for y := 0; y < t.rows; y++ {
		row := make([]Cell, t.cols)
		for x := 0; x < t.cols; x++ {
			row[x] = toCell(t.t.Cell(x, y))
		}
		rows[y] = row
	}
	return rows
}

// captureScrollLocked compares the pre-write grid to the post-write grid and,
// if the screen scrolled up by k rows, pushes the k evicted top rows into
// history. Caller holds mu.
func (t *Term) captureScrollLocked(before [][]Cell) {
	rows := len(before)
	if rows == 0 || rowBlank(before[0]) {
		return // nothing meaningful fell off the top
	}
	after := t.gridLocked()
	if len(after) != rows {
		return // a resize raced the write; skip this capture
	}
	k := scrollAmount(before, after)
	for i := 0; i < k; i++ {
		t.pushHistoryLocked(before[i])
	}
}

// scrollAmount returns k>0 when the screen scrolled up by k rows between before
// and after: rows [k:] of before now appear at [:rows-k] of after. The last
// overlapping row is excluded from the check because that is where the write
// that caused the scroll places its new content. It returns the largest such k.
func scrollAmount(before, after [][]Cell) int {
	rows := len(before)
	best := 0
	for k := 1; k < rows; k++ {
		cmp := rows - k - 1
		if cmp < 1 {
			cmp = rows - k
		}
		match := true
		for i := 0; i < cmp; i++ {
			if !rowEqual(before[k+i], after[i]) {
				match = false
				break
			}
		}
		if match {
			best = k
		}
	}
	return best
}

func (t *Term) pushHistoryLocked(row []Cell) {
	if t.histLimit <= 0 {
		return
	}
	cp := append([]Cell(nil), row...)
	t.history = append(t.history, cp)
	if len(t.history) > t.histLimit {
		t.history = t.history[len(t.history)-t.histLimit:]
	}
}

func rowEqual(a, b []Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func rowBlank(row []Cell) bool {
	for _, c := range row {
		if c.Ch != ' ' && c.Ch != 0 {
			return false
		}
	}
	return true
}

func toCell(g vt10x.Glyph) Cell {
	fg, bg, attr := convColor(g.FG), convColor(g.BG), convAttr(g.Mode)
	if g.Mode&vt10x.AttrWideTail != 0 {
		// Spacer after a double-width glyph: no character, zero width.
		return Cell{Ch: 0, FG: fg, BG: bg, Attr: attr, Width: 0}
	}
	ch := g.Char
	if ch == 0 {
		ch = ' '
	}
	width := uint8(1)
	if g.Mode&vt10x.AttrWide != 0 {
		width = 2
	}
	return Cell{Ch: ch, FG: fg, BG: bg, Attr: attr, Width: width}
}

// convColor maps a vt10x.Color to our uint32 encoding.
func convColor(c vt10x.Color) uint32 {
	switch {
	case c == vt10x.DefaultFG || c == vt10x.DefaultBG || c == vt10x.DefaultCursor:
		return ColorDefault
	case uint32(c) < 256:
		return uint32(c)
	case uint32(c) < 1<<24:
		return uint32(c)
	default:
		return ColorDefault
	}
}

// convAttr maps vt10x's internal glyph flag bits to our Attr bits. vt10x lays
// them out as reverse, underline, bold, gfx, italic, blink from bit 0.
func convAttr(mode int16) uint16 {
	const (
		vtReverse   = 1 << 0
		vtUnderline = 1 << 1
		vtBold      = 1 << 2
		vtItalic    = 1 << 4
		vtBlink     = 1 << 5
	)
	var a uint16
	if mode&vtReverse != 0 {
		a |= AttrReverse
	}
	if mode&vtUnderline != 0 {
		a |= AttrUnderline
	}
	if mode&vtBold != 0 {
		a |= AttrBold
	}
	if mode&vtItalic != 0 {
		a |= AttrItalic
	}
	if mode&vtBlink != 0 {
		a |= AttrBlink
	}
	return a
}
