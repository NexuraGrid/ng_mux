package vterm

import "testing"

// cellAt feeds s to a fresh terminal and returns the cell at (x, 0).
func cellAt(t *testing.T, s string, x int) Cell {
	t.Helper()
	term := New(20, 2, nil)
	if _, err := term.Write([]byte(s)); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap := term.Snapshot()
	return snap.At(x, 0)
}

// Dim (SGR 2), hidden (8) and strikethrough (9) are kept on the cells they
// style, and their resets (22, 28, 29) clear them, as in a plain terminal.
func TestDimHiddenStrikeAttributes(t *testing.T) {
	const s = "\x1b[2mA\x1b[22mB\x1b[8mC\x1b[28mD\x1b[9mE\x1b[29mF\x1b[1;2mG\x1b[22mH"
	for _, tt := range []struct {
		x    int
		want uint16
	}{
		{0, AttrDim}, {1, 0},
		{2, AttrHidden}, {3, 0},
		{4, AttrStrike}, {5, 0},
		{6, AttrBold | AttrDim}, {7, 0}, // 22 clears both bold and dim
	} {
		if got := cellAt(t, s, tt.x).Attr; got != tt.want {
			t.Errorf("cell %d (%q) attr = %#x, want %#x", tt.x, rune('A'+tt.x), got, tt.want)
		}
	}
}

// Reverse video keeps the cell's own colors and only marks it reversed; the
// real terminal does the swap. Swapping here as well cancelled it out.
func TestReverseKeepsColors(t *testing.T) {
	c := cellAt(t, "\x1b[31;42;7mX", 0)
	if c.FG != 1 || c.BG != 2 || c.Attr != AttrReverse {
		t.Fatalf("cell = fg %d bg %d attr %#x, want fg 1 bg 2 attr reverse", c.FG, c.BG, c.Attr)
	}
}

// Bold does not turn colors 0-7 into their bright variants: whether bold text
// is also brightened is the real terminal's setting.
func TestBoldKeepsColor(t *testing.T) {
	c := cellAt(t, "\x1b[1;31mX", 0)
	if c.FG != 1 || c.Attr != AttrBold {
		t.Fatalf("cell = fg %d attr %#x, want fg 1 (red) attr bold", c.FG, c.Attr)
	}
}

// DECSCUSR (CSI Ps SP q) sets the cursor shape apps like vim and Claude Code
// switch between; CSI Ps q without the space is a different command (DECLL)
// and leaves it alone.
func TestCursorStyleFollowsDECSCUSR(t *testing.T) {
	term := New(20, 2, nil)
	for _, tt := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"\x1b[5 q", 5},
		{"\x1b[1q", 5},
		{"\x1b[2 q", 2},
		{"\x1b[0 q", 0},
	} {
		term.Write([]byte(tt.in))
		if got := term.Snapshot().CursorStyle; got != tt.want {
			t.Fatalf("after %q CursorStyle = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// Scrollback keeps what a line really showed: trailing spaces with a
// background color stay colored, and the rest of a short line reads back as
// plain blanks.
func TestScrollbackKeepsColoredTrailingSpaces(t *testing.T) {
	term := New(10, 2, nil)
	term.Write([]byte("ab\x1b[41m  \x1b[0m\r\nx\r\ny\r\nz"))
	if term.HistoryLen() < 1 {
		t.Fatal("nothing scrolled into history")
	}
	snap := term.ScrollbackView(term.HistoryLen(), 2) // row 0: oldest history line
	for x, want := range []Cell{
		{Ch: 'a', FG: ColorDefault, BG: ColorDefault, Width: 1},
		{Ch: 'b', FG: ColorDefault, BG: ColorDefault, Width: 1},
		{Ch: ' ', FG: ColorDefault, BG: 1, Width: 1},
		{Ch: ' ', FG: ColorDefault, BG: 1, Width: 1},
		blankCell(),
		blankCell(),
	} {
		if got := snap.At(x, 0); got != want {
			t.Errorf("history cell %d = %+v, want %+v", x, got, want)
		}
	}
}

// An empty CSI parameter means its default (ECMA-48): 0 for SGR, so fzf's
// "ESC[;1;38;5;161;48;5;236m" is reset-then-style. It used to stop parsing at
// the empty parameter, dropping every color fzf draws (B15: `zi` colors).
func TestEmptySGRParameterIsReset(t *testing.T) {
	for _, tt := range []struct {
		name, in string
		x        int
		fg, bg   uint32
		attr     uint16
	}{
		{"fzf pointer", "\x1b[;1;38;5;161;48;5;236mX", 0, 161, 236, AttrBold},
		{"leading empty resets", "\x1b[1;31mA\x1b[;32mB", 1, 2, ColorDefault, 0},
		{"middle empty resets", "\x1b[1;;31mX", 0, 1, ColorDefault, 0},
		{"trailing empty resets", "\x1b[1;31;mX", 0, ColorDefault, ColorDefault, 0},
		{"bare SGR still resets", "\x1b[1;31mA\x1b[mB", 1, ColorDefault, ColorDefault, 0},
	} {
		c := cellAt(t, tt.in, tt.x)
		if c.FG != tt.fg || c.BG != tt.bg || c.Attr != tt.attr {
			t.Errorf("%s: cell = fg %d bg %d attr %#x, want fg %d bg %d attr %#x",
				tt.name, c.FG, c.BG, c.Attr, tt.fg, tt.bg, tt.attr)
		}
	}
}

// For cursor moves an empty parameter takes that command's own default, so
// "ESC[;5H" is row 1, column 5 and "ESC[2;H" is row 2, column 1.
func TestEmptyCursorParameterUsesDefault(t *testing.T) {
	for _, tt := range []struct {
		in   string
		x, y int
	}{
		{"\x1b[;5HX", 4, 0},
		{"\x1b[2;HX", 0, 1},
		{"abc\x1b[HX", 0, 0}, // no parameters at all: unchanged
	} {
		term := New(20, 3, nil)
		if _, err := term.Write([]byte(tt.in)); err != nil {
			t.Fatalf("write: %v", err)
		}
		snap := term.Snapshot()
		if got := snap.At(tt.x, tt.y).Ch; got != 'X' {
			t.Errorf("%q: cell (%d,%d) = %q, want 'X'", tt.in, tt.x, tt.y, got)
		}
	}
}

// Mode and scroll-region commands also read an empty parameter as default:
// "ESC[?;25l" still hides the cursor and "ESC[;3r" is rows 1-3.
func TestEmptyModeAndRegionParameters(t *testing.T) {
	term := New(20, 5, nil)
	if _, err := term.Write([]byte("\x1b[?;25l")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if snap := term.Snapshot(); snap.CurVisible {
		t.Error(`"ESC[?;25l" left the cursor visible`)
	}

	term = New(20, 5, nil)
	// Region rows 1-3; Z sits below it and must not move when it scrolls.
	if _, err := term.Write([]byte("\x1b[;3r\x1b[5;1HZ\x1b[1;1HA\r\nB\r\nC\r\nD")); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap := term.Snapshot()
	if got := snap.At(0, 0).Ch; got != 'B' {
		t.Errorf("row 0 = %q, want 'B' (A scrolled out of the 3-row region)", got)
	}
	if got := snap.At(0, 4).Ch; got != 'Z' {
		t.Errorf("row 4 = %q, want 'Z' (outside the region)", got)
	}
}
