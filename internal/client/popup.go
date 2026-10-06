package client

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// popupRowKind says how a popup row uses its column.
type popupRowKind uint8

const (
	// rowEntry is a key (or short label) in the accent column and what it does.
	rowEntry popupRowKind = iota
	// rowSpan ignores the key column and runs its text across the column (the
	// "+N more" note, or a blank spacer when text is empty).
	rowSpan
	// rowHeader is a section heading: the title followed by a dim "─" rule to
	// the column's width.
	rowHeader
)

// popupRow is one body line of a popup column.
type popupRow struct {
	key, text string
	kind      popupRowKind
}

// popupCol is one column of a popup's body: its rows and the display widths of
// the key and text columns inside it.
type popupCol struct {
	rows        []popupRow
	keyW, textW int
}

// width is the column's display width: key + "  " + text.
func (c popupCol) width() int { return c.keyW + 2 + c.textW }

// popup is the one look every client-side panel shares (the prefix
// cheat-sheet, the Ctrl-b m session help):
//
//	┌─ title ─────────────────────────────────┐
//	│ Section ──────────   Section ────────── │
//	│ key   what it does   key   what it does │
//	│ +N more                                 │
//	└─ footer ────────────────────────────────┘
//
// The body is one or more columns side by side, gap columns apart, then an
// optional full-width note. It is laid out as plain text by lines (what the
// tests and the sizing use) and coloured from a theme.Palette by paint, which
// walks the very same segments, so the two can never disagree. Every width is
// in display columns.
type popup struct {
	title, footer string
	cols          []popupCol
	gap           int
	note          string
}

// bodyRows is how many lines the columns take: the tallest column.
func (p popup) bodyRows() int {
	n := 0
	for _, c := range p.cols {
		n = max(n, len(c.rows))
	}
	return n
}

// height is the popup's full height in lines, frame included.
func (p popup) height() int {
	h := p.bodyRows() + 2
	if p.note != "" {
		h++
	}
	return h
}

// innerWidth is the width between "│ " and " │".
func (p popup) innerWidth() int {
	w := 0
	for i, c := range p.cols {
		if i > 0 {
			w += p.gap
		}
		w += c.width()
	}
	return w
}

// width is the popup's full width in columns: "│ " + body + " │".
func (p popup) width() int { return p.innerWidth() + 4 }

// segStyle picks which palette style a segment is painted in.
type segStyle uint8

const (
	segFrame segStyle = iota
	segTitle
	segKey
	segText
	segHead
	segRule
)

// popupSeg is a run of text in one style. lines joins the text; paint wraps
// each run in its style.
type popupSeg struct {
	s     string
	style segStyle
}

// border is a top or bottom frame line with label embedded after "─", e.g.
// "┌─ label ────┐".
func (p popup) border(left, right string, label string, labelStyle segStyle) []popupSeg {
	inner := p.width() - 2
	if label == "" || inner < 6 {
		return []popupSeg{{left + strings.Repeat("─", inner) + right, segFrame}}
	}
	// Keep at least one "─" between the label and the corner.
	label = " " + fitWidth(label, min(vterm.StringWidth(label), inner-4)) + " "
	used := 1 + vterm.StringWidth(label)
	return []popupSeg{
		{left + "─", segFrame},
		{label, labelStyle},
		{strings.Repeat("─", inner-used) + right, segFrame},
	}
}

// cell is row r laid out to exactly the column's width.
func (c popupCol) cell(r popupRow) []popupSeg {
	w := c.width()
	switch r.kind {
	case rowSpan:
		return []popupSeg{{fitWidth(r.text, w), segText}}
	case rowHeader:
		tw := vterm.StringWidth(r.text)
		if tw+2 > w {
			return []popupSeg{{fitWidth(r.text, w), segHead}}
		}
		return []popupSeg{{r.text, segHead}, {" ", segText}, {strings.Repeat("─", w-tw-1), segRule}}
	default:
		return []popupSeg{{fitWidth(r.key, c.keyW), segKey}, {"  " + fitWidth(r.text, c.textW), segText}}
	}
}

// segments lays the popup out as one run of segments per line.
func (p popup) segments() [][]popupSeg {
	out := make([][]popupSeg, 0, p.height())
	out = append(out, p.border("┌", "┐", p.title, segTitle))
	for i := 0; i < p.bodyRows(); i++ {
		line := []popupSeg{{"│", segFrame}, {" ", segText}}
		for j, c := range p.cols {
			if j > 0 {
				line = append(line, popupSeg{strings.Repeat(" ", p.gap), segText})
			}
			if i < len(c.rows) {
				line = append(line, c.cell(c.rows[i])...)
			} else {
				line = append(line, popupSeg{strings.Repeat(" ", c.width()), segText})
			}
		}
		out = append(out, append(line, popupSeg{" ", segText}, popupSeg{"│", segFrame}))
	}
	if p.note != "" {
		out = append(out, []popupSeg{
			{"│", segFrame}, {" " + fitWidth(p.note, p.innerWidth()) + " ", segText}, {"│", segFrame},
		})
	}
	return append(out, p.border("└", "┘", p.footer, segText))
}

// lines renders the popup as equal-width plain-text lines (no ANSI).
func (p popup) lines() []string {
	segs := p.segments()
	out := make([]string, len(segs))
	for i, line := range segs {
		var b strings.Builder
		for _, s := range line {
			b.WriteString(s.s)
		}
		out[i] = b.String()
	}
	return out
}

// paint writes the popup at 1-based (row, col) in pal's popup colours and
// leaves the terminal's attributes reset.
func (p popup) paint(b *strings.Builder, row, col int, pal *theme.Palette) {
	styles := [...]string{
		segFrame: sgr(pal.PopupFrame),
		segTitle: sgr(pal.PopupTitle),
		segKey:   sgr(pal.PopupKey),
		segText:  sgr(pal.PopupText),
		segHead:  sgr(pal.PopupHead),
		segRule:  sgr(pal.PopupRule),
	}
	for i, line := range p.segments() {
		fmt.Fprintf(b, "\x1b[%d;%dH", row+i, col)
		cur := -1
		for _, s := range line {
			if int(s.style) != cur {
				b.WriteString(styles[s.style])
				cur = int(s.style)
			}
			b.WriteString(s.s)
		}
	}
	b.WriteString("\x1b[0m")
}

// erase blanks the popup's area again at 1-based (row, col).
func (p popup) erase(b *strings.Builder, row, col int) {
	blank := strings.Repeat(" ", p.width())
	b.WriteString("\x1b[0m")
	for i := 0; i < p.height(); i++ {
		fmt.Fprintf(b, "\x1b[%d;%dH%s", row+i, col, blank)
	}
}

// fitWidth left-justifies s to exactly w display columns, truncating with an
// ellipsis when it is too wide. A wide rune is never split; a column it would
// straddle is padded with a space instead.
func fitWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	n := vterm.StringWidth(s)
	if n <= w {
		return s + strings.Repeat(" ", w-n)
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := vterm.RuneWidth(r)
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteString("…")
	used++
	return b.String() + strings.Repeat(" ", w-used)
}

// sgr is the escape sequence selecting st from a clean state: attributes, then
// colours. Only the 16 standard colours are emitted as their short codes;
// theme palettes never use more.
func sgr(st theme.Style) string {
	var b strings.Builder
	b.WriteString("\x1b[0")
	if st.Attr&vterm.AttrBold != 0 {
		b.WriteString(";1")
	}
	if st.Attr&vterm.AttrUnderline != 0 {
		b.WriteString(";4")
	}
	if st.Attr&vterm.AttrReverse != 0 {
		b.WriteString(";7")
	}
	b.WriteString(sgrColour(st.FG, 30, 90, "38"))
	b.WriteString(sgrColour(st.BG, 40, 100, "48"))
	b.WriteByte('m')
	return b.String()
}

func sgrColour(c, base, brightBase int, extended string) string {
	switch {
	case c < 0 || c == theme.Default:
		return ";" + strconv.Itoa(base+9)
	case c < 8:
		return ";" + strconv.Itoa(base+c)
	case c < 16:
		return ";" + strconv.Itoa(brightBase+c-8)
	default:
		return ";" + extended + ";5;" + strconv.Itoa(c&0xff)
	}
}
