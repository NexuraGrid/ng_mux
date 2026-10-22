package vterm

import (
	"strings"
	"testing"
	"time"
)

// Regression probes from the terminal-fidelity verification (odd/tasks/
// terminal-fidelity.md): each feeds bytes to a fresh terminal and checks one
// observable result.

func TestRegressionSGRResets(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want uint16 // attr of the cell at column 1
	}{
		{"\x1b[2;8;9mX\x1b[0mY", 0},
		{"\x1b[1;2mX\x1b[22mY", 0},
		{"\x1b[2mX\x1b[28;29mY", AttrDim},
		{"\x1b[8;9mX\x1b[28mY", AttrStrike},
	} {
		if got := cellAt(t, tt.in, 1).Attr; got != tt.want {
			t.Errorf("%q: attr = %#x, want %#x", tt.in, got, tt.want)
		}
	}
}

func TestRegressionCursorStyleBounds(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int
	}{
		{"\x1b[7 q", 6},
		{"\x1b[99 q", 6},
		{"\x1b[5 q\x1bc", 0},
		{"\x1b[5q", 0},
		{"\x1b[?5 q", 0},
	} {
		term := New(20, 2, nil)
		term.Write([]byte(tt.in))
		if got := term.Snapshot().CursorStyle; got != tt.want {
			t.Errorf("%q: CursorStyle = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestRegressionSyncNestedAndResize(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("A\x1b[?2026hB\x1b[?2026hC"))
	if got := row0(term); got != "A" {
		t.Fatalf("nested start row = %q, want %q", got, "A")
	}
	term.Resize(30, 2)
	if got := row0(term); got != "ABC" {
		t.Fatalf("after resize row = %q, want the live %q", got, "ABC")
	}
}

func TestRegressionSyncStaysDirtyUntilTimeout(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("A\x1b[?2026hB"))
	term.Snapshot()
	if !term.Dirty() {
		t.Fatal("a pane mid-update must stay dirty so it repaints after the timeout")
	}
	time.Sleep(syncTimeout + 50*time.Millisecond)
	if got := row0(term); got != "AB" {
		t.Fatalf("after timeout row = %q, want %q", got, "AB")
	}
}

func TestRegressionDECRQMReplies(t *testing.T) {
	var out syncBuf
	term := New(20, 2, &out)
	defer term.Close()
	term.Write([]byte("\x1b[?2026$p\x1b[?2026h\x1b[?2026$p\x1b[?1$p"))
	want := "\x1b[?2026;2$y\x1b[?2026;1$y\x1b[?1;0$y"
	deadline := time.Now().Add(time.Second)
	for out.String() != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := out.String(); got != want {
		t.Fatalf("replies = %q, want %q", got, want)
	}
}

func TestRegressionInsertAndRepeatEdges(t *testing.T) {
	for _, tt := range []struct {
		in, want string
	}{
		{"abcdefghij\r\x1b[4hXY", "XYabcdefgh"},
		{"\x1b[3b", ""},
		{"中\x1b[2b", "中中中"},
	} {
		term := New(10, 2, nil)
		term.Write([]byte(tt.in))
		got := strings.ReplaceAll(row0(term), "\x00", "")
		if got != tt.want {
			t.Errorf("%q: row = %q, want %q", tt.in, got, tt.want)
		}
	}

	term := New(10, 2, nil)
	done := make(chan struct{})
	go func() { term.Write([]byte("a\x1b[65535b")); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("REP with a huge count did not finish")
	}
}

func TestRegressionClipboardEncodings(t *testing.T) {
	for _, tt := range []struct {
		in, want string
	}{
		{"\x1b]52;c;aGVsbG8\x07", "hello"},
		{"\x1b]52;;aGk=\x1b\\", "hi"},
		{"\x1b]52;c;!!!\x07", ""},
	} {
		term := New(20, 2, nil)
		term.Write([]byte(tt.in))
		if got := term.TakeClipboard(); got != tt.want {
			t.Errorf("%q: clipboard = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRegressionScrollbackKeepsStyledBlanksAndWideTail(t *testing.T) {
	term := New(8, 2, nil)
	term.Write([]byte("x\x1b[1m  \x1b[0m\r\nabcdef中\r\n\r\n\r\n"))
	snap := term.ScrollbackView(term.HistoryLen(), 2)
	if c := snap.At(1, 0); c.Ch != ' ' || c.Attr != AttrBold {
		t.Errorf("bold blank = %+v, want a bold space", c)
	}
	if c := snap.At(7, 1); c.Width != 0 {
		t.Errorf("wide glyph spacer = %+v, want width 0", c)
	}
}
