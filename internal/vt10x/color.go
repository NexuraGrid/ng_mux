package vt10x

// ANSI color values
const (
	Black Color = iota
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	LightGrey
	DarkGrey
	LightRed
	LightGreen
	LightYellow
	LightBlue
	LightMagenta
	LightCyan
	White
)

// Default colors are potentially distinct to allow for special behavior.
// For example, a transparent background. Otherwise, the simple case is to
// map default colors to another color.
const (
	DefaultFG Color = 1<<24 + iota
	DefaultBG
	DefaultCursor
)

// Color maps to the ANSI colors [0, 16) and the xterm colors [16, 256), the
// Default* colors, or a truecolor value tagged with rgbFlag.
type Color uint32

// rgbFlag marks a Color as a packed 0xRRGGBB truecolor value. Without it,
// #0000c8 would be the number 200 and read back as palette color 200.
const rgbFlag Color = 1 << 25

// RGB returns the truecolor Color r, g, b (each 0-255).
func RGB(r, g, b int) Color { return rgbFlag | Color(r<<16|g<<8|b) }

// IsRGB reports whether c is a truecolor value made by RGB.
func (c Color) IsRGB() bool { return c&rgbFlag != 0 }

// Packed returns a truecolor value as 0xRRGGBB.
func (c Color) Packed() uint32 { return uint32(c) & 0xffffff }

// ANSI returns true if Color is within [0, 16).
func (c Color) ANSI() bool {
	return (c < 16)
}
