package render

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

func snapWith(cols, rows int, text string) *vterm.Snapshot {
	s := &vterm.Snapshot{Cols: cols, Rows: rows, Cells: make([]vterm.Cell, cols*rows)}
	for i := range s.Cells {
		s.Cells[i] = vterm.Cell{Ch: ' ', FG: vterm.ColorDefault, BG: vterm.ColorDefault}
	}
	for i, r := range text {
		if i >= cols {
			break
		}
		s.Cells[i] = vterm.Cell{Ch: r, FG: vterm.ColorDefault, BG: vterm.ColorDefault}
	}
	return s
}

func TestComposePlacesPaneContentAndStatus(t *testing.T) {
	pv := PaneView{
		ID:     1,
		Rect:   layout.Rect{X: 0, Y: 0, W: 20, H: 5},
		Snap:   snapWith(20, 5, "hello"),
		Active: true,
	}
	f := Compose(20, 6, []PaneView{pv}, "STATUS", DefaultStatusStyle)

	if got := rowString(f, 0); !strings.HasPrefix(got, "hello") {
		t.Errorf("row 0 = %q, want it to start with 'hello'", got)
	}
	if got := rowString(f, 5); !strings.HasPrefix(got, "STATUS") {
		t.Errorf("status row = %q, want it to start with 'STATUS'", got)
	}
}

func TestComposeDrawsDividerBetweenPanes(t *testing.T) {
	left := PaneView{ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 10, H: 5}, Snap: snapWith(10, 5, ""), Active: true}
	right := PaneView{ID: 2, Rect: layout.Rect{X: 11, Y: 0, W: 10, H: 5}, Snap: snapWith(10, 5, "")}
	f := Compose(21, 6, []PaneView{left, right}, "", DefaultStatusStyle)

	// Column 10 is the uncovered gap: it must be a vertical divider.
	if got := f.at(10, 2).Ch; got != '│' {
		t.Errorf("divider cell (10,2) = %q, want '│'", got)
	}
}

func TestPaintFullThenNoOpDiff(t *testing.T) {
	f1 := Compose(10, 3, []PaneView{{ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 10, H: 2}, Snap: snapWith(10, 2, "abc"), Active: true}}, "s", DefaultStatusStyle)
	full := Paint(nil, f1)
	if !strings.Contains(string(full), "\x1b[2J") {
		t.Errorf("first paint should clear the screen")
	}
	if !strings.Contains(string(full), "abc") {
		t.Errorf("first paint should contain the pane text; got %q", full)
	}

	// Re-painting the identical frame should emit no cell writes.
	same := Paint(f1, f1)
	if strings.Contains(string(same), "abc") {
		t.Errorf("no-op diff should not rewrite unchanged cells; got %q", same)
	}
}

func TestPaintDiffOnlyChangedCells(t *testing.T) {
	base := []PaneView{{ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 10, H: 2}, Snap: snapWith(10, 2, "abc"), Active: true}}
	f1 := Compose(10, 3, base, "s", DefaultStatusStyle)
	f2 := Compose(10, 3, []PaneView{{ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 10, H: 2}, Snap: snapWith(10, 2, "aXc"), Active: true}}, "s", DefaultStatusStyle)

	out := string(Paint(f1, f2))
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("same-size diff should not full-clear")
	}
	if !strings.Contains(out, "X") {
		t.Errorf("diff should write the changed rune; got %q", out)
	}
	if strings.Count(out, "a") > 0 {
		t.Errorf("diff should not rewrite the unchanged 'a'; got %q", out)
	}
}

func rowString(f *Frame, y int) string {
	var b strings.Builder
	for x := 0; x < f.Cols; x++ {
		b.WriteRune(f.at(x, y).Ch)
	}
	return b.String()
}

func TestComposeBadgeCentredInPane(t *testing.T) {
	pv := PaneView{
		ID:    1,
		Rect:  layout.Rect{X: 0, Y: 0, W: 21, H: 7},
		Snap:  snapWith(21, 7, ""),
		Badge: " 3 ",
	}
	f := Compose(21, 8, []PaneView{pv}, "", DefaultStatusStyle)

	// Centred: (21-3)/2 = 9 on row 7/2 = 3.
	if got := rowString(f, 3); got[9:12] != " 3 " {
		t.Fatalf("row 3 = %q, want %q at column 9", got, " 3 ")
	}
	// An unfocused pane's badge is the theme's badge pill.
	c := f.at(10, 3)
	want := theme.Dark().Badge
	if c.Ch != '3' || c.FG != uint32(want.FG) || c.BG != uint32(want.BG) || c.Attr != want.Attr {
		t.Fatalf("badge digit cell = %+v, want '3' styled %+v", c, want)
	}
}

func TestComposeNoBadgeWhenEmpty(t *testing.T) {
	pv := PaneView{ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 21, H: 7}, Snap: snapWith(21, 7, "")}
	f := Compose(21, 8, []PaneView{pv}, "", DefaultStatusStyle)
	if got := strings.TrimSpace(rowString(f, 3)); got != "" {
		t.Fatalf("row 3 = %q, want blank when no badge is set", got)
	}
}

// wideSnap builds a 1-row snapshot: a wide glyph at column 0 (+ its spacer at
// column 1), then the runes of tail starting at column 2.
func wideSnap(cols int, wide rune, tail string) *vterm.Snapshot {
	s := &vterm.Snapshot{Cols: cols, Rows: 1, Cells: make([]vterm.Cell, cols)}
	for i := range s.Cells {
		s.Cells[i] = vterm.Cell{Ch: ' ', FG: vterm.ColorDefault, BG: vterm.ColorDefault, Width: 1}
	}
	s.Cells[0] = vterm.Cell{Ch: wide, FG: vterm.ColorDefault, BG: vterm.ColorDefault, Width: 2}
	s.Cells[1] = vterm.Cell{Ch: 0, FG: vterm.ColorDefault, BG: vterm.ColorDefault, Width: 0}
	for i, r := range tail {
		if 2+i < cols {
			s.Cells[2+i] = vterm.Cell{Ch: r, FG: vterm.ColorDefault, BG: vterm.ColorDefault, Width: 1}
		}
	}
	return s
}

func TestComposeWideGlyphSpansTwoColumns(t *testing.T) {
	pv := PaneView{ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 6, H: 1}, Snap: wideSnap(6, '名', "ab")}
	f := Compose(6, 2, []PaneView{pv}, "", DefaultStatusStyle)

	if c := f.at(0, 0); c.Ch != '名' || c.Width != 2 {
		t.Fatalf("cell(0,0) = %#U width=%d, want '名' width 2", c.Ch, c.Width)
	}
	if c := f.at(1, 0); c.Width != 0 {
		t.Fatalf("cell(1,0) width = %d, want 0 (spacer)", c.Width)
	}
	if c := f.at(2, 0); c.Ch != 'a' {
		t.Fatalf("cell(2,0) = %#U, want 'a' — content after a wide glyph must not shift", c.Ch)
	}
}

// stripANSI returns just the printable runes of a Paint stream.
func stripANSI(b []byte) string {
	var out []rune
	for i := 0; i < len(b); i++ {
		if b[i] == 0x1b {
			for i < len(b) && b[i] != 'H' && b[i] != 'm' && b[i] != 'J' && b[i] != 'l' && b[i] != 'h' {
				i++
			}
			continue
		}
		r, sz := utf8.DecodeRune(b[i:])
		if sz > 1 || (b[i] >= ' ' && b[i] < 0x7f) {
			out = append(out, r)
		}
		i += sz - 1
	}
	return string(out)
}

func TestPaintWideGlyphEmittedOnceAndAligned(t *testing.T) {
	next := Compose(6, 2, []PaneView{{
		ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 6, H: 1}, Snap: wideSnap(6, '名', "XY"),
	}}, "", DefaultStatusStyle)

	got := stripANSI(Paint(nil, next))
	// The wide glyph is followed directly by X and Y: no spurious space from
	// the spacer cell would leave "名 XY" instead.
	if !strings.Contains(got, "名XY") {
		t.Fatalf("painted text = %q, want it to contain %q", got, "名XY")
	}
	if strings.Count(got, "名") != 1 {
		t.Fatalf("wide glyph emitted %d times, want 1 (text=%q)", strings.Count(got, "名"), got)
	}
}

func TestPaintWideGlyphDiffLeavesLeadAlone(t *testing.T) {
	mk := func(tail string) *Frame {
		return Compose(6, 2, []PaneView{{
			ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 6, H: 1}, Snap: wideSnap(6, '名', tail),
		}}, "", DefaultStatusStyle)
	}
	prev, next := mk("X"), mk("Y")

	out := Paint(prev, next)
	got := stripANSI(out)
	if strings.Contains(got, "名") {
		t.Fatalf("diff re-emitted the unchanged wide glyph: %q", got)
	}
	if !strings.Contains(got, "Y") {
		t.Fatalf("diff did not emit the changed cell: %q", got)
	}
	// The change is at column 2 (0-indexed) -> the move must target column 3.
	if !bytes.Contains(out, []byte("\x1b[1;3H")) {
		t.Fatalf("expected a cursor move to row 1 col 3, got %q", out)
	}
}

// Where a vertical and a horizontal divider meet, the border uses a proper
// junction glyph instead of leaving a gap or drawing a cross.
func TestBordersDrawJunctions(t *testing.T) {
	// Left pane full height; right column split into top and bottom.
	panes := []PaneView{
		{ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 5, H: 5}, Snap: snapWith(5, 5, "")},
		{ID: 2, Rect: layout.Rect{X: 6, Y: 0, W: 5, H: 2}, Snap: snapWith(5, 2, ""), Active: true},
		{ID: 3, Rect: layout.Rect{X: 6, Y: 3, W: 5, H: 2}, Snap: snapWith(5, 2, "")},
	}
	f := Compose(11, 6, panes, "", DefaultStatusStyle)
	cases := []struct {
		x, y int
		want rune
	}{
		{5, 0, '│'},
		{5, 2, '├'},
		{5, 4, '│'},
		{8, 2, '─'},
	}
	for _, c := range cases {
		if got := f.at(c.x, c.y).Ch; got != c.want {
			t.Errorf("cell (%d,%d) = %q, want %q", c.x, c.y, got, c.want)
		}
	}
}

// A titled pane's title sits on the row above its rectangle, embedded in the
// border line; content and cursor stay inside the rectangle.
func TestComposeDrawsPaneTitles(t *testing.T) {
	left := PaneView{ID: 1, Rect: layout.Rect{X: 0, Y: 1, W: 12, H: 4}, Snap: snapWith(12, 4, "top"),
		ShowTitle: true, TitleNum: 0, Title: "bash"}
	right := PaneView{ID: 2, Rect: layout.Rect{X: 13, Y: 1, W: 12, H: 4}, Snap: snapWith(12, 4, ""),
		Active: true, ShowTitle: true, TitleNum: 1, Title: "a-very-long-title"}
	right.Snap.CurVisible = true
	f := Compose(25, 6, []PaneView{left, right}, "", DefaultStatusStyle)

	if got, want := rowString(f, 0), "─ 0 bash ───┬─ 1 a-ver… ─"; got != want {
		t.Fatalf("title row = %q, want %q", got, want)
	}
	if got := rowString(f, 1); !strings.HasPrefix(got, "top") {
		t.Errorf("content row = %q, want pane content right under the title", got)
	}
	if f.CurX != 13 || f.CurY != 1 {
		t.Errorf("cursor at (%d,%d), want the pane's own origin (13,1)", f.CurX, f.CurY)
	}

	dark := theme.Dark()
	if c := f.at(15, 0); c.Ch != '1' || c.FG != uint32(dark.TitleActive.FG) || c.BG != uint32(dark.TitleActive.BG) {
		t.Errorf("focused title cell = %+v, want the active pill", c)
	}
	if c := f.at(14, 0); c.Ch != ' ' || c.BG != uint32(dark.TitleActive.BG) {
		t.Errorf("pill padding cell = %+v, want the active pill", c)
	}
	if c := f.at(2, 0); c.Ch != '0' || c.Attr != 0 || c.FG != vterm.ColorDefault {
		t.Errorf("unfocused title cell = %+v, want plain text", c)
	}
	if c := f.at(24, 0); c.Ch != '─' || c.FG != uint32(dark.BorderActive.FG) {
		t.Errorf("focused title line = %+v, want the active border colour", c)
	}
}

// A pane too narrow for even " N " keeps a bare border line.
func TestComposePaneTitleTooNarrow(t *testing.T) {
	pv := PaneView{ID: 1, Rect: layout.Rect{X: 0, Y: 1, W: 4, H: 2}, Snap: snapWith(4, 2, ""),
		ShowTitle: true, TitleNum: 12, Title: "x"}
	f := Compose(4, 4, []PaneView{pv}, "", DefaultStatusStyle)
	if got := rowString(f, 0); got != "────" {
		t.Fatalf("title row = %q, want a plain line", got)
	}
}

// The mono theme draws chrome with the terminal's default colours only, and
// marks the focused pane's borders bold instead of green.
func TestComposeMonoThemeUsesNoColour(t *testing.T) {
	mono := theme.Mono()
	left := PaneView{ID: 1, Rect: layout.Rect{X: 0, Y: 0, W: 5, H: 4}, Snap: snapWith(5, 4, ""), Badge: " 0 "}
	right := PaneView{ID: 2, Rect: layout.Rect{X: 6, Y: 0, W: 5, H: 4}, Snap: snapWith(5, 4, ""), Active: true,
		Overlay: " COPY 1/2 "}
	segs := []StatusSegment{{Text: " s ", FG: mono.Session.FG, BG: mono.Session.BG, Attr: mono.Session.Attr}}
	f := ComposeThemedInto(nil, 11, 5, []PaneView{left, right}, segs, &mono)

	for i, c := range f.Cells {
		if c.FG != vterm.ColorDefault || c.BG != vterm.ColorDefault {
			t.Fatalf("cell %d = %+v uses a colour under the mono theme", i, c)
		}
	}
	if c := f.at(5, 1); c.Ch != '│' || c.Attr&vterm.AttrBold == 0 {
		t.Errorf("divider next to the focused pane = %+v, want bold", c)
	}
	if c := f.at(2, 2); c.Ch != '0' || c.Attr&vterm.AttrReverse == 0 {
		t.Errorf("mono badge = %+v, want reverse video", c)
	}
	if c := f.at(0, 4); c.Attr&vterm.AttrReverse != 0 {
		t.Errorf("mono session pill = %+v, want normal video inside the reversed bar", c)
	}
	if c := f.at(10, 4); c.Attr&vterm.AttrReverse == 0 {
		t.Errorf("mono bar = %+v, want reverse video", c)
	}
}
