package vterm

import "testing"

func TestIncompleteRuneTail(t *testing.T) {
	tests := []struct {
		name string
		p    []byte
		want int
	}{
		{"empty", nil, 0},
		{"ascii", []byte("abc"), 0},
		{"complete 2-byte rune", []byte("é"), 0}, // C3 A9
		{"incomplete 2-byte rune, 1 of 2", []byte{0xC3}, 1},
		{"complete 3-byte rune", []byte("世"), 0}, // E4 B8 96
		{"incomplete 3-byte rune, 1 of 3", []byte{0xE4}, 1},
		{"incomplete 3-byte rune, 2 of 3", []byte{0xE4, 0xB8}, 2},
		{"complete 4-byte rune", []byte("🙂"), 0}, // F0 9F 99 82
		{"incomplete 4-byte rune, 1 of 4", []byte{0xF0}, 1},
		{"incomplete 4-byte rune, 2 of 4", []byte{0xF0, 0x9F}, 2},
		{"incomplete 4-byte rune, 3 of 4", []byte{0xF0, 0x9F, 0x99}, 3},
		{"ascii after a complete multi-byte rune", []byte("éx"), 0},
		{"incomplete rune after a complete ascii prefix", append([]byte("hi"), 0xE4, 0xB8), 2},
		{"lone continuation byte, no lead in window", []byte{0x80}, 0},
		{"invalid lead byte", []byte{0xFF}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := incompleteRuneTail(tt.p); got != tt.want {
				t.Errorf("incompleteRuneTail(%v) = %d, want %d", tt.p, got, tt.want)
			}
		})
	}
}

// TestWriteCarriesIncompleteRuneAcrossCalls is the sharp end of
// incompleteRuneTail: it drives Term.Write itself, splitting a multi-byte
// rune across two calls the way a pump chunk boundary (or a real pty read
// boundary) can, and checks the emulator ends up with the intact character
// rather than vt10x's own mojibake for a rune it never saw whole.
func TestWriteCarriesIncompleteRuneAcrossCalls(t *testing.T) {
	tests := []struct {
		name  string
		glyph string // the multi-byte rune under test, as a Go string literal
		split int    // bytes fed in the first Write call
	}{
		{"2-byte rune split 1/2", "é", 1},
		{"3-byte rune split 1/3", "世", 1},
		{"3-byte rune split 2/3", "世", 2},
		{"4-byte rune split 1/4", "🙂", 1},
		{"4-byte rune split 2/4", "🙂", 2},
		{"4-byte rune split 3/4", "🙂", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := []rune(tt.glyph)[0]
			raw := []byte(tt.glyph)

			term := New(10, 2, nil)
			if _, err := term.Write(raw[:tt.split]); err != nil {
				t.Fatalf("first Write: %v", err)
			}
			if _, err := term.Write(raw[tt.split:]); err != nil {
				t.Fatalf("second Write: %v", err)
			}
			// Trailing bytes after the rune so the cursor has moved past it
			// and Snapshot sees a settled cell, not a pending combining state.
			if _, err := term.Write([]byte(" ")); err != nil {
				t.Fatalf("trailing Write: %v", err)
			}

			snap := term.Snapshot()
			if got := snap.At(0, 0).Ch; got != want {
				t.Fatalf("cell(0,0) = %q, want %q (rune split %d/%d bytes across two Write calls)",
					got, want, tt.split, len(raw))
			}
		})
	}
}

// TestWriteRuneSplitAcrossThreeCalls checks the pending buffer keeps working
// when a single rune is fed one byte at a time, not just split in two.
func TestWriteRuneSplitAcrossThreeCalls(t *testing.T) {
	raw := []byte("🙂") // F0 9F 99 82
	want := []rune("🙂")[0]

	term := New(10, 2, nil)
	for _, b := range raw {
		if _, err := term.Write([]byte{b}); err != nil {
			t.Fatalf("Write byte %#x: %v", b, err)
		}
	}
	if _, err := term.Write([]byte(" ")); err != nil {
		t.Fatalf("trailing Write: %v", err)
	}

	snap := term.Snapshot()
	if got := snap.At(0, 0).Ch; got != want {
		t.Fatalf("cell(0,0) = %q, want %q", got, want)
	}
}
