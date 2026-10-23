package vterm

import "testing"

// B21: a truecolor value is RGB whatever its numeric value, so #0000c8 must
// not come out as palette color 200, and the colon SGR forms (ITU T.416) must
// set the same colors as their semicolon forms without dropping what follows.
func TestTruecolorAndColonSGR(t *testing.T) {
	for _, tt := range []struct {
		name, in string
		fg, bg   uint32
		attr     uint16
	}{
		{"rgb below 256 stays rgb", "\x1b[38;2;0;0;200mX", RGB(0, 0, 200), ColorDefault, 0},
		{"rgb bg below 256 stays rgb", "\x1b[48;2;0;0;5mX", ColorDefault, RGB(0, 0, 5), 0},
		{"black rgb is not palette 0", "\x1b[38;2;0;0;0mX", RGB(0, 0, 0), ColorDefault, 0},
		{"colon rgb with colorspace", "\x1b[38:2::0:0:200mX", RGB(0, 0, 200), ColorDefault, 0},
		{"colon rgb without colorspace", "\x1b[38:2:255:128:0mX", RGB(255, 128, 0), ColorDefault, 0},
		{"colon rgb with colorspace id", "\x1b[48:2:1:10:20:30mX", ColorDefault, RGB(10, 20, 30), 0},
		{"colon indexed", "\x1b[38:5:161;48:5:236mX", 161, 236, 0},
		{"colon keeps later params", "\x1b[38:2::0:0:200;1mX", RGB(0, 0, 200), ColorDefault, AttrBold},
		{"colon underline style", "\x1b[4:3mX", ColorDefault, ColorDefault, AttrUnderline},
		{"colon underline off", "\x1b[4m\x1b[4:0mX", ColorDefault, ColorDefault, 0},
		// Unchanged forms.
		{"rgb above 256", "\x1b[38;2;255;0;0mX", RGB(255, 0, 0), ColorDefault, 0},
		{"indexed", "\x1b[38;5;200;48;5;4mX", 200, 4, 0},
		{"basic", "\x1b[31;42mX", 1, 2, 0},
	} {
		c := cellAt(t, tt.in, 0)
		if c.FG != tt.fg || c.BG != tt.bg || c.Attr != tt.attr {
			t.Errorf("%s: cell = fg %#x bg %#x attr %#x, want fg %#x bg %#x attr %#x",
				tt.name, c.FG, c.BG, c.Attr, tt.fg, tt.bg, tt.attr)
		}
	}
}

// An OSC 4 palette override is an RGB value too: overriding color 1 with
// #000080 must not turn red cells into palette color 128.
func TestPaletteOverrideIsRGB(t *testing.T) {
	c := cellAt(t, "\x1b]4;1;rgb:00/00/80\x1b\\\x1b[31mX", 0)
	if c.FG != RGB(0, 0, 0x80) {
		t.Fatalf("fg = %#x, want %#x", c.FG, RGB(0, 0, 0x80))
	}
}
