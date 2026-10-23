package render

import (
	"bytes"
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// B21: a truecolor below 256 must be re-emitted as truecolor, not as the
// palette index with the same number.
func TestWriteColorKeepsTruecolorApartFromPalette(t *testing.T) {
	for _, tt := range []struct {
		name  string
		color uint32
		fg    bool
		want  string
	}{
		{"rgb #0000c8", vterm.RGB(0, 0, 200), true, ";38;2;0;0;200"},
		{"rgb black bg", vterm.RGB(0, 0, 0), false, ";48;2;0;0;0"},
		{"rgb #ff0000", vterm.RGB(255, 0, 0), true, ";38;2;255;0;0"},
		{"palette 200", 200, true, ";38;5;200"},
		{"basic red", 1, true, ";31"},
		{"bright bg", 9, false, ";101"},
	} {
		var b bytes.Buffer
		writeColor(&b, tt.color, tt.fg)
		if got := b.String(); got != tt.want {
			t.Errorf("%s: writeColor = %q, want %q", tt.name, got, tt.want)
		}
	}
}
