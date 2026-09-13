package vt10x

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestCSIParse(t *testing.T) {
	var csi csiEscape
	csi.reset()
	csi.buf = []byte("s")
	csi.parse()
	if csi.mode != 's' || csi.arg(0, 17) != 17 || len(csi.args) != 0 {
		t.Fatal("CSI parse mismatch")
	}

	csi.reset()
	csi.buf = []byte("31T")
	csi.parse()
	if csi.mode != 'T' || csi.arg(0, 0) != 31 || len(csi.args) != 1 {
		t.Fatal("CSI parse mismatch")
	}

	csi.reset()
	csi.buf = []byte("48;2f")
	csi.parse()
	if csi.mode != 'f' || csi.arg(0, 0) != 48 || csi.arg(1, 0) != 2 || len(csi.args) != 2 {
		t.Fatal("CSI parse mismatch")
	}

	csi.reset()
	csi.buf = []byte("?25l")
	csi.parse()
	if csi.mode != 'l' || csi.arg(0, 0) != 25 || csi.priv != true || len(csi.args) != 1 {
		t.Fatal("CSI parse mismatch")
	}
}

// TestCSIParseClampsHostileParams asserts the parser itself never hands a
// negative or absurdly large value to a handler: parse() is the single choke
// point every CSI parameter passes through.
func TestCSIParseClampsHostileParams(t *testing.T) {
	var csi csiEscape

	csi.reset()
	csi.buf = []byte("-5@")
	csi.parse()
	if got := csi.arg(0, 1); got != 0 {
		t.Fatalf("negative param: arg(0) = %d, want 0 (clamped)", got)
	}

	csi.reset()
	csi.buf = []byte("999999999999@")
	csi.parse()
	if got := csi.arg(0, 1); got != maxCSIArg {
		t.Fatalf("huge param: arg(0) = %d, want %d (clamped)", got, maxCSIArg)
	}
}

// TestCSIHostileParamsDoNotPanic feeds every handler that derives a slice
// index or loop bound from its parameter a battery of hostile inputs:
// negative, zero, huge, many params, and an oversized param string. None of
// these should ever panic, and the grid must stay addressable afterwards
// (String() succeeds, cursor stays in bounds).
func TestCSIHostileParamsDoNotPanic(t *testing.T) {
	// @ ICH, P DCH, X ECH, I CHT, Z CBT, L IL, M DL, S SU, T SD: derive a
	// slice range or loop count from their sole parameter.
	// A B C D: cursor motion by <n>. G H d r: absolute position. J K: clear
	// with a mode selector, not a count, but still parsed through the same
	// hostile path.
	letters := []byte{'@', 'P', 'X', 'I', 'Z', 'L', 'M', 'S', 'T', 'A', 'B', 'C', 'D', 'G', 'H', 'd', 'r', 'J', 'K'}

	params := []string{
		"-5",
		"0",
		"999999999",
		"-999999999",
		strings.Repeat("9", 300),       // 300-byte single param
		"-1;-2;-3;-4;-5",               // many negative params
		strings.Repeat("5;", 60) + "5", // many params
	}

	for _, letter := range letters {
		for _, p := range params {
			p := p
			letter := letter
			t.Run(fmt.Sprintf("%c/%s", letter, shortName(p)), func(t *testing.T) {
				term := New(WithSize(80, 24))
				seq := "\x1b[" + p + string(letter)
				assertWriteNeverPanics(t, term, seq)
			})
		}
	}
}

// TestCSIReproducers pins the two hostile sequences that were confirmed to
// panic vt10x before this fix: a negative ICH/DCH count reaches
// insertBlanks/deleteChars as a slice index, producing "slice bounds out of
// range [:85] with capacity 80" for an 80-column screen.
func TestCSIReproducers(t *testing.T) {
	for _, seq := range []string{"\x1b[-5@", "\x1b[-5P"} {
		t.Run(seq, func(t *testing.T) {
			term := New(WithSize(80, 24))
			assertWriteNeverPanics(t, term, seq)
		})
	}
}

// assertWriteNeverPanics writes seq into term and fails the test if it
// panics, errors unexpectedly, or leaves the cursor outside the grid.
func assertWriteNeverPanics(t *testing.T, term Terminal, seq string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic writing %q: %v", seq, r)
		}
	}()
	if _, err := term.Write([]byte(seq)); err != nil && err != io.EOF {
		t.Fatalf("write %q: unexpected error: %v", seq, err)
	}
	_ = term.String() // must not panic either
	cur := term.Cursor()
	cols, rows := term.Size()
	if cur.X < 0 || cur.X >= cols || cur.Y < 0 || cur.Y >= rows {
		t.Fatalf("write %q left cursor out of bounds: (%d,%d), size %dx%d", seq, cur.X, cur.Y, cols, rows)
	}
}

// shortName keeps subtest names readable when a param is a 300-byte string.
func shortName(s string) string {
	if len(s) > 24 {
		return fmt.Sprintf("%s...(%dB)", s[:12], len(s))
	}
	return s
}

// FuzzWrite feeds arbitrary byte streams into a fresh terminal and asserts
// none of them can ever panic the emulator or leave the cursor out of
// bounds. Seeds include the two confirmed reproducers plus a mix of ordinary
// and hostile real-world sequences; fuzzing explores the space around them.
func FuzzWrite(f *testing.F) {
	seeds := []string{
		"\x1b[-5@",
		"\x1b[-5P",
		"Hello, world!\n",
		"\x1b[2J\x1b[H",
		"\x1b[999999999@",
		"\x1b[999999999P",
		"\x1b[-1;-2;-3H",
		"\x1b[8;24r",
		"\x1b[1;31mred\x1b[0m",
		"\x1b[" + strings.Repeat("9", 300) + "X",
		"\x1b[20h\r\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		term := New(WithSize(80, 24))
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", data, r)
			}
		}()
		_, _ = term.Write([]byte(data))
		cur := term.Cursor()
		cols, rows := term.Size()
		if cur.X < 0 || cur.X >= cols || cur.Y < 0 || cur.Y >= rows {
			t.Fatalf("cursor out of bounds after %q: (%d,%d), size %dx%d", data, cur.X, cur.Y, cols, rows)
		}
	})
}
