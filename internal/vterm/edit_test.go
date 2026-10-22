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
