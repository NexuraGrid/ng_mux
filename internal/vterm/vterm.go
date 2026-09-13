// Package vterm wraps a headless VT100/xterm emulator (internal/vt10x, an
// in-tree fork of github.com/hinshun/vt10x) behind a small, stable surface:
// feed it the raw bytes a pty produces, ask it for a rectangular snapshot of
// cells plus the cursor. The rest of ngmux never touches vt10x directly, so the
// emulator can be swapped later without churn.
//
// vt10x itself keeps no scrollback, so this package reconstructs one, but it
// does so natively: vt10x calls back (vt10x.WithScrollback) with each line as
// it scrolls off the top of the main screen, and Write does no work beyond
// that hook to detect scrolling. Capture is suppressed by vt10x itself while
// the child is on the alternate screen (vim, less, htop), matching tmux.
// History is a fixed-capacity ring buffer that reuses its backing arrays once
// full, so steady-state log output allocates nothing per scrolled line.
package vterm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"unicode/utf8"

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
// device-query responses (DA, DSR, ...) and to feed onScrollOut every line
// that scrolls off the top of the main screen. It is the single constructor
// used by both New and Write's panic-recovery path, so a freshly recovered
// emulator never loses its scrollback capture wiring.
func newEmulator(cols, rows int, reply io.Writer, onScrollOut func(ln []vt10x.Glyph)) vt10x.Terminal {
	opts := []vt10x.TerminalOption{vt10x.WithSize(cols, rows), vt10x.WithScrollback(onScrollOut)}
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

	// histBuf is a ring buffer holding the scrollback: oldest line at
	// histHead, histLen of its len(histBuf) (== histCap) slots in use. Each
	// row is exactly cols wide at push time. histCap<=0 disables capture
	// (pushHistory becomes a no-op). Overwriting the oldest slot in place,
	// rather than reallocating, is what makes steady-state scrolling
	// allocation-free.
	histCap  int
	histBuf  [][]Cell
	histHead int
	histLen  int

	// scrolledTotal counts every line ever pushed into history (not just the
	// ones currently retained). Copy-mode uses it to keep its view anchored
	// while more output arrives and lines it is looking at age out the
	// bottom. It only advances when a line is actually pushed, so it stays at
	// zero while capture is disabled.
	scrolledTotal uint64

	// pending buffers the trailing bytes of a multi-byte UTF-8 rune left
	// incomplete at the end of a Write call, so they can be prepended to the
	// next one instead of being fed to vt10x half-formed. Without this, a rune
	// split across a pty read (or a pump chunk boundary) would reach vt10x's
	// own Write as an unterminated encoding: vt10x has no cross-call buffer of
	// its own, so it either logs each stray byte as "invalid utf8 sequence" or
	// silently drops the last one, corrupting the character either way. At
	// most 3 bytes: the longest incomplete prefix of a 4-byte encoding.
	// Caller holds mu.
	pending []byte

	// dirty is set whenever bytes arrive or the grid is resized, and read by
	// Dirty() without taking mu: the server's broadcaster polls every pane's
	// Dirty() while holding its own session lock, and a pane mid-Write must
	// never make that block (see Write/Dirty). Snapshot clears it. A fresh
	// Term is dirty so its first frame is always drawn.
	dirty atomic.Bool
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
	t := &Term{reply: reply, cols: cols, rows: rows, histCap: 2000}
	t.dirty.Store(true)
	t.histBuf = make([][]Cell, t.histCap)
	t.t = newEmulator(cols, rows, reply, t.pushHistory)
	return t
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
// Shrinking or growing preserves the newest min(n, HistoryLen()) lines, in
// order.
func (t *Term) SetHistoryLimit(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n < 0 {
		n = 0
	}
	if n == t.histCap {
		return
	}
	oldBuf, oldHead, oldLen := t.histBuf, t.histHead, t.histLen
	keep := oldLen
	if keep > n {
		keep = n
	}
	newBuf := make([][]Cell, n)
	for i := 0; i < keep; i++ {
		// oldest-first logical index of the newest `keep` rows
		logical := oldLen - keep + i
		newBuf[i] = oldBuf[(oldHead+logical)%len(oldBuf)]
	}
	t.histBuf = newBuf
	t.histHead = 0
	t.histLen = keep
	t.histCap = n
}

// Write feeds pty output into the emulator. Scrollback capture happens inside
// vt10x itself (see newEmulator's onScrollOut hook), so Write does little more
// work than parsing the payload once, plus holding back a trailing incomplete
// UTF-8 rune (see the pending field) so a caller is free to split one logical
// burst across several Write calls at arbitrary byte boundaries.
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
			t.t = newEmulator(t.cols, t.rows, t.reply, t.pushHistory)
			t.dirty.Store(true)
			t.pending = t.pending[:0]
			err = fmt.Errorf("%w: %v\n%s", ErrEmulatorPanic, r, debug.Stack())
		}
	}()

	if len(p) == 0 {
		return 0, nil
	}
	t.dirty.Store(true)
	t.answerDeviceQueries(p)

	buf := p
	if len(t.pending) > 0 {
		buf = append(append(make([]byte, 0, len(t.pending)+len(p)), t.pending...), p...)
	}

	feed := buf
	if tail := incompleteRuneTail(buf); tail > 0 {
		feed = buf[:len(buf)-tail]
	}

	if len(feed) > 0 {
		if _, werr := t.t.Write(feed); werr != nil {
			return len(p), werr
		}
	}

	if held := buf[len(feed):]; len(held) > 0 {
		t.pending = append(t.pending[:0], held...)
	} else {
		t.pending = t.pending[:0]
	}

	return len(p), nil
}

// incompleteRuneTail reports how many trailing bytes of p form a UTF-8
// encoding left incomplete at the end of the slice (0 if p ends on a
// complete rune, or on genuinely invalid bytes that vt10x should just log and
// discard as it always has). It scans back at most 3 bytes — the longest
// incomplete prefix of a 4-byte encoding — for the start of the last rune,
// then checks whether the bytes from there to the end already form a full
// encoding. utf8.FullRune treats an invalid encoding as "full" (it converts
// to a width-1 error rune on its own), so genuinely malformed bytes are never
// held back indefinitely — only a valid prefix that is still waiting on more
// continuation bytes is.
func incompleteRuneTail(p []byte) int {
	n := len(p)
	limit := 3
	if n < limit {
		limit = n
	}
	for i := 1; i <= limit; i++ {
		b := p[n-i]
		if !utf8.RuneStart(b) {
			continue
		}
		if utf8.FullRune(p[n-i:]) {
			return 0
		}
		return i
	}
	return 0
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
	t.dirty.Store(true)
}

// Dirty reports whether bytes have arrived or the grid was resized since the
// last Snapshot. A fresh Term is dirty.
//
// It reads the flag without taking mu, unlike every other method here, so a
// pane mid-Write (which can hold mu for a while feeding a large burst into
// the emulator) never makes Dirty wait: the server's broadcaster calls it for
// every pane, under its own session lock, to decide whether to repaint —
// blocking there would stall every other pane's keystrokes and every other
// session's frames behind one busy pane.
func (t *Term) Dirty() bool {
	return t.dirty.Load()
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
	return t.histLen
}

// ScrolledTotal is the number of lines ever pushed into history, including
// ones since evicted by the ring's capacity. It only advances when a line is
// actually pushed (never while capture is disabled). Copy-mode uses it to
// anchor its view by count rather than by index while output keeps arriving.
func (t *Term) ScrolledTotal() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scrolledTotal
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

	// Cleared first thing inside the locked section, before the copy below,
	// not after: Write and Resize both also take mu, so neither can run
	// concurrently with this method's body and no bytes are ever missed
	// either way. Clearing early is the more conservative order — if a future
	// dirty-setting path ever stopped taking mu, clearing late could still
	// discard a mark for bytes this copy never saw, where clearing early can
	// only ever cost one extra (harmless) repaint.
	t.dirty.Store(false)

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
//
// It reads live rows directly from the emulator (one t.t.Lock for the whole
// call) instead of snapshotting the entire grid first, so only the result
// itself is allocated.
func (t *Term) ScrollbackView(offset, viewRows int) Snapshot {
	if viewRows <= 0 {
		viewRows = 1
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	histLen := t.histLen
	total := histLen + t.rows

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

	t.t.Lock()
	defer t.t.Unlock()
	for r := 0; r < viewRows; r++ {
		for x := 0; x < cols; x++ {
			snap.Cells[r*cols+x] = blankCell()
		}
		src := start + r
		if src < 0 || src >= total {
			continue
		}
		if src < histLen {
			row := t.historyRow(src)
			for x := 0; x < cols && x < len(row); x++ {
				snap.Cells[r*cols+x] = row[x]
			}
			continue
		}
		y := src - histLen
		for x := 0; x < cols; x++ {
			snap.Cells[r*cols+x] = toCell(t.t.Cell(x, y))
		}
	}
	return snap
}

// historyRow returns the oldest-first logical row i (0 <= i < t.histLen).
// Caller holds t.mu.
func (t *Term) historyRow(i int) []Cell {
	return t.histBuf[(t.histHead+i)%len(t.histBuf)]
}

// pushHistory converts one scrolled-off vt10x line into a Cell row and pushes
// it into the ring, reusing the evicted slot's backing array when the row
// width is unchanged (the steady-state case: fixed pane size, allocation
// free). It is vt10x's onScrollOut hook, so vt10x's own concurrency rules
// apply: it runs synchronously from inside t.t.Write or t.t.Resize, both of
// which vterm only ever calls while already holding t.mu, and the slice it
// receives is invalid once this function returns.
func (t *Term) pushHistory(g []vt10x.Glyph) {
	if t.histCap <= 0 {
		return
	}
	t.scrolledTotal++

	var slot int
	if t.histLen < len(t.histBuf) {
		slot = (t.histHead + t.histLen) % len(t.histBuf)
		t.histLen++
	} else {
		slot = t.histHead
		t.histHead = (t.histHead + 1) % len(t.histBuf)
	}

	row := t.histBuf[slot]
	if cap(row) < len(g) {
		row = make([]Cell, len(g))
	} else {
		row = row[:len(g)]
	}
	for i, gl := range g {
		row[i] = toCell(gl)
	}
	t.histBuf[slot] = row
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
