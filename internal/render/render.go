// Package render composites the individual pane screens into one frame the
// size of the client terminal, draws pane borders and a status bar, and turns
// consecutive frames into a minimal stream of ANSI escape sequences.
//
// The server renders; the client just writes the bytes it receives to stdout.
// Keeping all emulation and diffing server-side makes the client trivial and
// identical on every platform.
package render

import (
	"bytes"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// Cell is one composited character position. Width is 2 for the lead cell of a
// double-width glyph and 0 for the spacer cell that follows it; 1 otherwise.
type Cell struct {
	Ch    rune
	FG    uint32
	BG    uint32
	Attr  uint16
	Width uint8
}

var blank = Cell{Ch: ' ', FG: vterm.ColorDefault, BG: vterm.ColorDefault, Width: 1}

// Frame is a full client-sized screen plus the cursor state to show.
type Frame struct {
	Cols, Rows int
	Cells      []Cell
	CurX, CurY int
	CurVisible bool

	// covered is border-detection scratch, reused across ComposeInto calls on
	// the same Frame so a per-tick repaint allocates nothing.
	covered []bool
}

// NewFrame returns a blank frame of the given size.
func NewFrame(cols, rows int) *Frame {
	f := &Frame{Cols: cols, Rows: rows, Cells: make([]Cell, cols*rows)}
	for i := range f.Cells {
		f.Cells[i] = blank
	}
	return f
}

// reset makes f a blank cols x rows frame, reusing its Cells buffer when it is
// already the right length.
func (f *Frame) reset(cols, rows int) {
	f.Cols, f.Rows = cols, rows
	f.CurX, f.CurY, f.CurVisible = 0, 0, false
	if len(f.Cells) != cols*rows {
		f.Cells = make([]Cell, cols*rows)
	}
	for i := range f.Cells {
		f.Cells[i] = blank
	}
}

// CopyFrom makes f an exact copy of src's cells and cursor, reusing f's cell
// buffer when it is large enough. A client keeps such a copy of the last frame
// it was sent, so later diffs never depend on a session's reusable buffers.
func (f *Frame) CopyFrom(src *Frame) {
	f.Cols, f.Rows = src.Cols, src.Rows
	f.CurX, f.CurY, f.CurVisible = src.CurX, src.CurY, src.CurVisible
	if cap(f.Cells) < len(src.Cells) {
		f.Cells = make([]Cell, len(src.Cells))
	} else {
		f.Cells = f.Cells[:len(src.Cells)]
	}
	copy(f.Cells, src.Cells)
}

// scratchCovered returns an all-false []bool of length n backed by f.covered.
func (f *Frame) scratchCovered(n int) []bool {
	if cap(f.covered) < n {
		f.covered = make([]bool, n)
	}
	f.covered = f.covered[:n]
	for i := range f.covered {
		f.covered[i] = false
	}
	return f.covered
}

func (f *Frame) set(x, y int, c Cell) {
	if x < 0 || y < 0 || x >= f.Cols || y >= f.Rows {
		return
	}
	f.Cells[y*f.Cols+x] = c
}

func (f *Frame) at(x, y int) Cell {
	if x < 0 || y < 0 || x >= f.Cols || y >= f.Rows {
		return blank
	}
	return f.Cells[y*f.Cols+x]
}

// PaneView is the input for one pane: where it sits and what it shows.
type PaneView struct {
	ID     layout.PaneID
	Rect   layout.Rect
	Snap   *vterm.Snapshot
	Active bool

	// Overlay, when non-empty, is printed as a pill at the pane's top-right
	// corner (used for the " COPY 12/340 " indicator).
	Overlay string
	// Badge, when non-empty, is printed as a pill centred in the pane (used by
	// display-panes to show each pane's index).
	Badge string
	// ShowTitle draws a title line on the row just above Rect (which the
	// caller keeps free of panes: the top row of the window, or the divider
	// above the pane) reading "─ <TitleNum> <Title> ────".
	ShowTitle bool
	TitleNum  int
	Title     string
	// Sel, when non-nil, highlights an inclusive cell range in the pane's own
	// coordinates (used by copy-mode selection).
	Sel *Selection
	// CopyCur, when non-nil, places the terminal cursor here (pane coords)
	// instead of at the emulator cursor (used by copy-mode).
	CopyCur *[2]int
}

// Selection is an inclusive rectangle-free range: from (X0,Y0) to (X1,Y1) in
// reading order. It need not be normalised; the renderer orders the endpoints.
type Selection struct {
	X0, Y0, X1, Y1 int
}

func (s Selection) contains(x, y, cols int) bool {
	a := s.Y0*cols + s.X0
	b := s.Y1*cols + s.X1
	if a > b {
		a, b = b, a
	}
	p := y*cols + x
	return p >= a && p <= b
}

// StatusStyle holds the status bar's base colours (xterm colour indices). It
// overrides the StatusFG/StatusBG of the dark theme for callers that do not
// pass a whole palette.
type StatusStyle struct {
	FG, BG int
}

// DefaultStatusStyle is the dark theme's bar: light-grey text on black.
var DefaultStatusStyle = StatusStyle{FG: 0, BG: 7}

// StatusSegment is a run of status-bar text with its own emphasis. FG and BG
// are xterm colour indices (or theme.Default); -1 means "inherit the bar's
// default style" (which is what a plain-string status uses for every cell).
// Attr adds attribute bits (bold, underline, …) on top of the bar's
// reverse-video base.
type StatusSegment struct {
	Text   string
	FG, BG int
	Attr   uint16
}

// InheritColour, used for StatusSegment.FG/BG, keeps the bar's default colour.
const InheritColour = theme.Inherit

// darkPalette is copied, never modified, by the StatusStyle entry points.
var darkPalette = theme.Dark()

// Compose builds a fresh frame of size cols x rows. The last row is the status
// bar; panes are laid out in rows [0, rows-1). status is the text shown in the
// bar.
func Compose(cols, rows int, panes []PaneView, status string, style StatusStyle) *Frame {
	return ComposeInto(nil, cols, rows, panes, status, style)
}

// ComposeInto is Compose that writes into dst, reusing dst's Cells buffer when
// it is already the right size. A nil or wrong-sized dst is allocated fresh.
// The returned frame (dst when reused) should be kept for the next call so the
// reuse actually happens.
func ComposeInto(dst *Frame, cols, rows int, panes []PaneView, status string, style StatusStyle) *Frame {
	return ComposeStyledInto(dst, cols, rows, panes,
		[]StatusSegment{{Text: status, FG: InheritColour, BG: InheritColour}}, style)
}

// ComposeStyledInto is ComposeInto with a segmented status bar: each segment
// carries its own emphasis (see StatusSegment). Segments are laid out left to
// right and the bar is padded to full width with the default style. Chrome
// other than the bar's base colours uses the dark theme.
func ComposeStyledInto(dst *Frame, cols, rows int, panes []PaneView, status []StatusSegment, style StatusStyle) *Frame {
	if style.FG == darkPalette.StatusFG && style.BG == darkPalette.StatusBG {
		return ComposeThemedInto(dst, cols, rows, panes, status, &darkPalette)
	}
	pal := darkPalette
	pal.StatusFG, pal.StatusBG = style.FG, style.BG
	return ComposeThemedInto(dst, cols, rows, panes, status, &pal)
}

// ComposeThemedInto is ComposeStyledInto with every piece of chrome (bar,
// borders, pane titles, badges, overlay) drawn from pal.
func ComposeThemedInto(dst *Frame, cols, rows int, panes []PaneView, status []StatusSegment, pal *theme.Palette) *Frame {
	f := dst
	if f == nil {
		f = &Frame{}
	}
	f.reset(cols, rows)

	contentRows := rows - 1
	if contentRows < 1 {
		contentRows = rows
	}

	covered := f.scratchCovered(cols * contentRows)
	markCovered := func(x, y int) {
		if x >= 0 && y >= 0 && x < cols && y < contentRows {
			covered[y*cols+x] = true
		}
	}

	var active *PaneView
	for i := range panes {
		p := &panes[i]
		if p.Active {
			active = p
		}
		r := p.Rect
		for row := 0; row < r.H; row++ {
			for col := 0; col < r.W; col++ {
				sx, sy := r.X+col, r.Y+row
				markCovered(sx, sy)
				var sc vterm.Cell
				if p.Snap != nil {
					sc = p.Snap.At(col, row)
				} else {
					sc = vterm.Cell{Ch: ' ', FG: vterm.ColorDefault, BG: vterm.ColorDefault, Width: 1}
				}
				cell := Cell{Ch: sc.Ch, FG: sc.FG, BG: sc.BG, Attr: sc.Attr, Width: sc.Width}
				if p.Sel != nil && p.Sel.contains(col, row, r.W) {
					cell.Attr ^= vterm.AttrReverse
				}
				f.set(sx, sy, cell)
			}
		}
		if p.Overlay != "" {
			ox := r.X + r.W - vterm.StringWidth(p.Overlay)
			if ox < r.X {
				ox = r.X
			}
			putText(f, ox, r.Y, r.X+r.W, p.Overlay, pal.Overlay)
		}
		if p.Badge != "" {
			bx := r.X + (r.W-vterm.StringWidth(p.Badge))/2
			if bx < r.X {
				bx = r.X
			}
			st := pal.Badge
			if p.Active {
				st = pal.BadgeActive
			}
			putText(f, bx, r.Y+r.H/2, r.X+r.W, p.Badge, st)
		}
	}

	drawBorders(f, covered, cols, contentRows, active, pal)
	for i := range panes {
		if panes[i].ShowTitle {
			drawTitle(f, &panes[i], pal)
		}
	}
	drawStatusSegments(f, status, pal.StatusFG, pal.StatusBG)

	switch {
	case active != nil && active.CopyCur != nil:
		f.CurX = active.Rect.X + active.CopyCur[0]
		f.CurY = active.Rect.Y + active.CopyCur[1]
		f.CurVisible = true
	case active != nil && active.Snap != nil && active.Snap.CurVisible:
		f.CurX = active.Rect.X + active.Snap.CurX
		f.CurY = active.Rect.Y + active.Snap.CurY
		f.CurVisible = true
	}
	return f
}

// colours resolves a chrome Style's FG/BG to cell colours. Inherit only means
// something on the status bar; anywhere else it falls back to the default.
func colours(st theme.Style) (fg, bg uint32) {
	fg, bg = vterm.ColorDefault, vterm.ColorDefault
	if st.FG >= 0 {
		fg = uint32(st.FG)
	}
	if st.BG >= 0 {
		bg = uint32(st.BG)
	}
	return fg, bg
}

// putText draws s in style st starting at column x of row y, never at or past
// column limit, and returns the column after the last cell drawn. A
// double-width rune that would straddle limit is not drawn.
func putText(f *Frame, x, y, limit int, s string, st theme.Style) int {
	for _, r := range s {
		nx := putRune(f, x, y, limit, r, st)
		if nx == x {
			break
		}
		x = nx
	}
	return x
}

// putRune is putText for a single rune; it returns x unchanged when the rune
// does not fit before limit.
func putRune(f *Frame, x, y, limit int, r rune, st theme.Style) int {
	w := vterm.RuneWidth(r)
	if w < 1 {
		w = 1
	}
	if x+w > limit {
		return x
	}
	fg, bg := colours(st)
	f.set(x, y, Cell{Ch: r, FG: fg, BG: bg, Attr: st.Attr, Width: uint8(w)})
	if w == 2 {
		f.set(x+1, y, Cell{Ch: 0, FG: fg, BG: bg, Attr: st.Attr, Width: 0})
	}
	return x + w
}

// drawTitle writes a pane's title pill onto the border line just above it:
//
//	─ 1 bash ──────
//
// The line itself is drawn by drawBorders (the row is uncovered, so it is a
// divider); this only overwrites the run after its first cell with
// " <num> <title> ", cutting the title with "…" when the pane is too narrow.
// It allocates nothing.
func drawTitle(f *Frame, p *PaneView, pal *theme.Palette) {
	r := p.Rect
	y := r.Y - 1
	if y < 0 || r.W < 4 {
		return
	}
	st := pal.Title
	if p.Active {
		st = pal.TitleActive
	}
	x := r.X + 1
	limit := r.X + r.W - 1 // keep a line cell at the right end

	var digits [20]byte
	num := strconv.AppendInt(digits[:0], int64(p.TitleNum), 10)
	if 1+len(num)+1 > limit-x {
		return
	}
	x = putRune(f, x, y, limit, ' ', st)
	for _, d := range num {
		x = putRune(f, x, y, limit, rune(d), st)
	}
	title := p.Title
	x = putRune(f, x, y, limit, ' ', st)
	if title == "" {
		return
	}
	// " " after the title, so the room for the title itself is one less.
	room := limit - x - 1
	if room < 1 {
		return
	}
	if vterm.StringWidth(title) <= room {
		x = putText(f, x, y, limit, title, st)
	} else {
		x = putText(f, x, y, x+room-1, title, st)
		x = putRune(f, x, y, limit, '…', st)
	}
	putRune(f, x, y, limit, ' ', st)
}

// borderGlyphs maps which neighbours a divider cell connects to (bit 0 up,
// 1 down, 2 left, 3 right) to its box-drawing rune. Every glyph is in CP437,
// so they render on old consoles and raster fonts too. A cell that connects
// along one side only (a divider touching the screen edge) is a plain line.
var borderGlyphs = [16]rune{
	0b0000: ' ',
	0b0001: '│', 0b0010: '│', 0b0011: '│',
	0b0100: '─', 0b1000: '─', 0b1100: '─',
	0b0101: '┘', 0b0110: '┐', 0b1001: '└', 0b1010: '┌',
	0b0111: '┤', 0b1011: '├', 0b1101: '┴', 0b1110: '┬',
	0b1111: '┼',
}

// drawBorders draws every divider cell. Those around the focused pane use
// pal.BorderActive (green in the dark theme, bold in mono) so the focus is
// obvious; the rest use the dim pal.BorderDim.
func drawBorders(f *Frame, covered []bool, cols, contentRows int, active *PaneView, pal *theme.Palette) {
	dimFG, _ := colours(pal.BorderDim)
	activeFG, _ := colours(pal.BorderActive)
	activeAdj := func(x, y int) bool {
		if active == nil {
			return false
		}
		r := active.Rect
		return (x >= r.X-1 && x <= r.X+r.W && y >= r.Y-1 && y <= r.Y+r.H)
	}
	cov := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < cols && y < contentRows && covered[y*cols+x]
	}
	// A divider cell is an uncovered cell touching a pane, diagonals included:
	// the junction where dividers cross touches panes only at its corners.
	divider := func(x, y int) bool {
		if x < 0 || y < 0 || x >= cols || y >= contentRows || covered[y*cols+x] {
			return false
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if cov(x+dx, y+dy) {
					return true
				}
			}
		}
		return false
	}
	for y := 0; y < contentRows; y++ {
		for x := 0; x < cols; x++ {
			if !divider(x, y) {
				continue
			}
			var mask int
			if divider(x, y-1) {
				mask |= 1
			}
			if divider(x, y+1) {
				mask |= 2
			}
			if divider(x-1, y) {
				mask |= 4
			}
			if divider(x+1, y) {
				mask |= 8
			}
			ch := borderGlyphs[mask]
			if ch == ' ' {
				// An isolated cell: fall back to the line its panes imply.
				if cov(x-1, y) || cov(x+1, y) {
					ch = '│'
				} else {
					ch = '─'
				}
			}
			c := Cell{Ch: ch, FG: dimFG, BG: vterm.ColorDefault, Attr: pal.BorderDim.Attr, Width: 1}
			if activeAdj(x, y) {
				c.FG, c.Attr = activeFG, pal.BorderActive.Attr
			}
			f.set(x, y, c)
		}
	}
}

// drawStatusSegments paints the bar's bottom row from left to right. Every cell
// keeps the bar's reverse-video base; a segment that overrides FG or BG opts out
// of reverse so its colour reads literally, and the side it leaves inherited
// takes the colour the bar visibly shows there (reverse swaps them), so a
// coloured word sits on the same background as the rest of the bar. Any tail
// past the last segment is filled with the default style.
func drawStatusSegments(f *Frame, segs []StatusSegment, statusFG, statusBG int) {
	y := f.Rows - 1
	if y < 0 {
		return
	}
	defFG, defBG := uint32(statusFG), uint32(statusBG)
	x := 0
	put := func(c Cell) {
		if x >= f.Cols {
			return
		}
		f.set(x, y, c)
		x++
	}
	// putRune lays one rune down, spanning two cells for a double-width glyph so
	// the bar's column bookkeeping (done with vterm.StringWidth) stays true.
	putRune := func(r rune, fg, bg uint32, attr uint16) {
		if vterm.RuneWidth(r) == 2 && x+1 < f.Cols {
			f.set(x, y, Cell{Ch: r, FG: fg, BG: bg, Attr: attr, Width: 2})
			f.set(x+1, y, Cell{Ch: 0, FG: fg, BG: bg, Attr: attr, Width: 0})
			x += 2
			return
		}
		put(Cell{Ch: r, FG: fg, BG: bg, Attr: attr, Width: 1})
	}
	for _, seg := range segs {
		fg, bg := defFG, defBG
		attr := uint16(vterm.AttrReverse)
		if seg.FG != InheritColour || seg.BG != InheritColour {
			// Without reverse, the visible text colour is defBG and the
			// visible background is defFG.
			fg, bg, attr = defBG, defFG, 0
			if seg.FG != InheritColour {
				fg = uint32(seg.FG)
			}
			if seg.BG != InheritColour {
				bg = uint32(seg.BG)
			}
		}
		attr |= seg.Attr
		for _, r := range seg.Text {
			putRune(r, fg, bg, attr)
		}
	}
	for x < f.Cols {
		put(Cell{Ch: ' ', FG: defFG, BG: defBG, Attr: vterm.AttrReverse})
	}
}

// Paint returns the ANSI byte stream that turns a terminal currently showing
// prev into one showing next. If prev is nil or differently sized, it does a
// full repaint. If nothing visible differs (cells, cursor position and
// visibility), it returns nil so an unchanged frame costs no bytes.
func Paint(prev, next *Frame) []byte {
	var b bytes.Buffer
	full := prev == nil || prev.Cols != next.Cols || prev.Rows != next.Rows

	b.WriteString("\x1b[?25l") // hide cursor while we draw

	if full {
		b.WriteString("\x1b[2J")
		prev = nil
	}

	var (
		curX, curY = -1, -1
		lastFG     = uint32(0xDEADBEEF)
		lastBG     = uint32(0xDEADBEEF)
		lastAttr   = uint16(0xFFFF)
		changed    bool
	)
	for y := 0; y < next.Rows; y++ {
		for x := 0; x < next.Cols; x++ {
			nc := next.at(x, y)
			// The spacer after a double-width glyph is not written: the
			// terminal advanced two columns when the wide rune was emitted.
			if nc.Width == 0 && x > 0 && next.at(x-1, y).Width == 2 {
				continue
			}
			if prev != nil && nc == prev.at(x, y) {
				continue
			}
			if curX != x || curY != y {
				b.WriteString("\x1b[")
				b.WriteString(strconv.Itoa(y + 1))
				b.WriteByte(';')
				b.WriteString(strconv.Itoa(x + 1))
				b.WriteByte('H')
				curX, curY = x, y
			}
			if nc.FG != lastFG || nc.BG != lastBG || nc.Attr != lastAttr {
				writeSGR(&b, nc)
				lastFG, lastBG, lastAttr = nc.FG, nc.BG, nc.Attr
			}
			ch := nc.Ch
			if ch == 0 {
				ch = ' '
			}
			var tmp [4]byte
			n := utf8.EncodeRune(tmp[:], ch)
			b.Write(tmp[:n])
			changed = true
			curX = x + 1
			if nc.Width == 2 {
				curX = x + 2 // the terminal advanced two columns
			}
		}
	}

	if prev != nil && !changed && prev.CurVisible == next.CurVisible &&
		(!next.CurVisible || (prev.CurX == next.CurX && prev.CurY == next.CurY)) {
		return nil
	}

	b.WriteString("\x1b[0m")
	if next.CurVisible {
		b.WriteString("\x1b[")
		b.WriteString(strconv.Itoa(next.CurY + 1))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(next.CurX + 1))
		b.WriteByte('H')
		b.WriteString("\x1b[?25h")
	}
	return b.Bytes()
}

// writeSGR emits a full "reset then set" SGR sequence for cell c.
func writeSGR(b *bytes.Buffer, c Cell) {
	b.WriteString("\x1b[0")
	if c.Attr&vterm.AttrBold != 0 {
		b.WriteString(";1")
	}
	if c.Attr&vterm.AttrItalic != 0 {
		b.WriteString(";3")
	}
	if c.Attr&vterm.AttrUnderline != 0 {
		b.WriteString(";4")
	}
	if c.Attr&vterm.AttrBlink != 0 {
		b.WriteString(";5")
	}
	if c.Attr&vterm.AttrReverse != 0 {
		b.WriteString(";7")
	}
	writeColor(b, c.FG, true)
	writeColor(b, c.BG, false)
	b.WriteByte('m')
}

func writeColor(b *bytes.Buffer, color uint32, fg bool) {
	if color == vterm.ColorDefault {
		if fg {
			b.WriteString(";39")
		} else {
			b.WriteString(";49")
		}
		return
	}
	switch {
	case color < 8:
		base := 30
		if !fg {
			base = 40
		}
		fmt.Fprintf(b, ";%d", base+int(color))
	case color < 16:
		base := 90
		if !fg {
			base = 100
		}
		fmt.Fprintf(b, ";%d", base+int(color-8))
	case color < 256:
		if fg {
			fmt.Fprintf(b, ";38;5;%d", color)
		} else {
			fmt.Fprintf(b, ";48;5;%d", color)
		}
	default: // packed 0xRRGGBB
		r, g, bl := (color>>16)&0xff, (color>>8)&0xff, color&0xff
		if fg {
			fmt.Fprintf(b, ";38;2;%d;%d;%d", r, g, bl)
		} else {
			fmt.Fprintf(b, ";48;2;%d;%d;%d", r, g, bl)
		}
	}
}
