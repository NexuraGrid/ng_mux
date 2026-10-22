package vterm

import "testing"

// Insert mode (IRM, CSI 4 h) shifts the rest of the line right instead of
// overwriting it, until CSI 4 l turns it off again.
func TestInsertModeShiftsLine(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("abc\x1b[1G\x1b[4hXY"))
	if got := row0(term); got != "XYabc" {
		t.Fatalf("with insert mode row = %q, want %q", got, "XYabc")
	}
	term.Write([]byte("\x1b[4lZ"))
	if got := row0(term); got != "XYZbc" {
		t.Fatalf("after CSI 4 l row = %q, want %q (overwrite again)", got, "XYZbc")
	}
}

// REP (CSI Ps b) repeats the last printed character Ps times; ncurses uses it
// to draw runs of the same character.
func TestRepeatLastCharacter(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("a-\x1b[4b|"))
	if got := row0(term); got != "a-----|" {
		t.Fatalf("row = %q, want %q", got, "a-----|")
	}
}

// A full reset forgets the last printed character, so REP after it repeats
// nothing.
func TestResetForgetsRepeatCharacter(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("a\x1bc\x1b[3b"))
	if got := row0(term); got != "" {
		t.Fatalf("row = %q, want empty", got)
	}
}

// A full reset (ESC c) clears the whole screen, including columns beyond the
// row count on a pane wider than it is tall.
func TestResetClearsWholeScreen(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("0123456789abcdef\x1bc"))
	if got := row0(term); got != "" {
		t.Fatalf("row after reset = %q, want empty", got)
	}
}
