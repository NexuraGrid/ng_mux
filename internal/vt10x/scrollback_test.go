package vt10x

import (
	"strings"
	"testing"
)

// glyphString reads a captured scrollback line as a trimmed string.
func glyphString(g []Glyph) string {
	var b strings.Builder
	for _, c := range g {
		ch := c.Char
		if ch == 0 {
			ch = ' '
		}
		b.WriteRune(ch)
	}
	return strings.TrimRight(b.String(), " ")
}

// scrollCapture collects every line handed to onScrollOut, copying it (the
// hook's slice is documented invalid once the call returns).
type scrollCapture struct {
	lines []string
}

func (c *scrollCapture) hook(ln []Glyph) {
	c.lines = append(c.lines, glyphString(ln))
}

func fillRows(t *testing.T, term Terminal, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := term.Write([]byte(prefix + itoaVT(i) + "\r\n")); err != nil {
			t.Fatalf("write row %d: %v", i, err)
		}
	}
}

func itoaVT(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func TestScrollUpHistoryLinefeedAtBottom(t *testing.T) {
	var capture scrollCapture
	term := New(WithSize(10, 4), WithScrollback(capture.hook))

	// 4 rows fill without scrolling (r0..r3); each line after that ends with
	// its own trailing "\r\n" at the bottom margin, so it scrolls immediately
	// and evicts the then-oldest row: r0 (written while printing r3), then
	// r1, r2, r3.
	fillRows(t, term, "r", 7)

	want := []string{"r0", "r1", "r2", "r3"}
	if len(capture.lines) != len(want) {
		t.Fatalf("captured %v, want %v", capture.lines, want)
	}
	for i, w := range want {
		if capture.lines[i] != w {
			t.Fatalf("captured[%d] = %q, want %q", i, capture.lines[i], w)
		}
	}
}

func TestScrollUpHistoryIND(t *testing.T) {
	var capture scrollCapture
	term := New(WithSize(10, 3), WithScrollback(capture.hook))

	term.Write([]byte("row0\r\nrow1\r\nrow2"))
	// Move to the bottom row explicitly, then send IND (ESC D): cursor is
	// already on the last row, so IND must scroll and capture row0.
	term.Write([]byte("\x1b[3;1H"))
	term.Write([]byte("\x1bD"))

	if len(capture.lines) != 1 || capture.lines[0] != "row0" {
		t.Fatalf("captured %v, want [row0]", capture.lines)
	}
}

func TestScrollUpHistoryCSIS(t *testing.T) {
	var capture scrollCapture
	term := New(WithSize(10, 4), WithScrollback(capture.hook))

	term.Write([]byte("a\r\nb\r\nc\r\nd"))
	term.Write([]byte("\x1b[2S")) // SU: scroll up 2, regardless of cursor position

	if len(capture.lines) != 2 || capture.lines[0] != "a" || capture.lines[1] != "b" {
		t.Fatalf("captured %v, want [a b]", capture.lines)
	}
}

func TestScrollUpHistoryNotFiredForPartialRegion(t *testing.T) {
	var capture scrollCapture
	term := New(WithSize(10, 12), WithScrollback(capture.hook))

	term.Write([]byte("\x1b[2;9r"))    // DECSTBM: scroll region rows 2..9 (top=1, bottom=8)
	term.Write([]byte("\x1b[9;1H"))    // move into the region's bottom margin
	term.Write([]byte("line\r\nline")) // linefeed at the bottom margin scrolls the region

	if len(capture.lines) != 0 {
		t.Fatalf("captured %v, want none: a partial DECSTBM region must never populate scrollback", capture.lines)
	}
}

func TestScrollUpHistoryNotFiredForDL(t *testing.T) {
	var capture scrollCapture
	term := New(WithSize(10, 4), WithScrollback(capture.hook))

	term.Write([]byte("a\r\nb\r\nc\r\nd"))
	term.Write([]byte("\x1b[1;1H")) // cursor to row0 (top of the full-screen region)
	term.Write([]byte("\x1b[M"))    // DL: delete 1 line - scrolls but must not touch history

	if len(capture.lines) != 0 {
		t.Fatalf("captured %v, want none: DL must never populate scrollback", capture.lines)
	}
}

func TestScrollUpHistoryNotFiredOnAltScreen(t *testing.T) {
	var capture scrollCapture
	term := New(WithSize(10, 3), WithScrollback(capture.hook))

	term.Write([]byte("\x1b[?1049h")) // enter alternate screen
	term.Write([]byte("x\r\ny\r\nz\r\nw"))

	if len(capture.lines) != 0 {
		t.Fatalf("captured %v, want none: the alternate screen must never populate scrollback", capture.lines)
	}
}

func TestScrollUpHistoryNoHookSet(t *testing.T) {
	term := New(WithSize(10, 3)) // no WithScrollback: must not panic
	fillRows(t, term, "n", 10)
}

func TestResizeShrinkEmitsSlidLines(t *testing.T) {
	var capture scrollCapture
	term := New(WithSize(10, 5), WithScrollback(capture.hook))

	// Fill exactly 5 rows so the cursor sits on the last row without any
	// scroll having happened yet.
	term.Write([]byte("l0\r\nl1\r\nl2\r\nl3\r\nl4"))

	term.Resize(10, 3) // shrink by 2: cursor.Y(4) - 3 + 1 = 2 lines slide off

	if len(capture.lines) != 2 || capture.lines[0] != "l0" || capture.lines[1] != "l1" {
		t.Fatalf("captured %v, want [l0 l1]", capture.lines)
	}
}

func TestResizeShrinkOnAltScreenDoesNotEmit(t *testing.T) {
	var capture scrollCapture
	term := New(WithSize(10, 5), WithScrollback(capture.hook))

	term.Write([]byte("\x1b[?1049h")) // enter alternate screen
	term.Write([]byte("l0\r\nl1\r\nl2\r\nl3\r\nl4"))

	term.Resize(10, 3)

	if len(capture.lines) != 0 {
		t.Fatalf("captured %v, want none: a resize on the alternate screen must not feed the main screen's scrollback", capture.lines)
	}
}
