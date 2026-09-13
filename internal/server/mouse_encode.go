package server

import (
	"fmt"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// wantsMouseTracking reports whether the app running in the pane asked for
// any flavour of mouse reporting (modes 9/1000/1002/1003). At most one of
// these bits is ever set at a time — vt10x clears the others whenever one is
// requested (see vt10x.State.setMode) — but the check tolerates more than one
// being set just in case.
func wantsMouseTracking(m vterm.InputModes) bool {
	return m.MouseX10 || m.MouseButton || m.MouseMotion || m.MouseAny
}

// forwardKindAllowed reports whether the app's tracking mode reports this
// kind of event, following xterm's mouse-mode semantics:
//   - X10 compatibility mode (9) reports button presses only; there is no
//     release at all in the original protocol. Real X10 has no wheel support
//     either, but every modern terminal (xterm included) reports a wheel
//     notch as a press-style event even in this mode, so we do the same
//     rather than silently eating wheel input for an X10-only app.
//   - Normal tracking mode (1000) adds release.
//   - Button-event tracking (1002) and any-event tracking (1003) add drag,
//     reported only while a button is held — this client only ever enables
//     modes 1000/1002/1006 (see internal/client/client.go), so a drag report
//     always has a button in it; any-motion-without-a-button (bare mode
//     1003 semantics) is never something this client can produce.
func forwardKindAllowed(m vterm.InputModes, kind string) bool {
	switch kind {
	case protocol.MousePress, protocol.MouseWheelUp, protocol.MouseWheelDown:
		return wantsMouseTracking(m)
	case protocol.MouseRelease:
		return m.MouseButton || m.MouseMotion || m.MouseAny
	case protocol.MouseDrag:
		return m.MouseMotion || m.MouseAny
	default:
		return false
	}
}

// encodeSGRMouse renders an SGR (mode 1006) mouse report: ESC [ < Cb ; Cx ;
// Cy, terminated with 'M' for press/drag/wheel or 'm' for release. x,y are
// 1-based, as the wire format requires.
func encodeSGRMouse(cb, x, y int, release bool) []byte {
	final := byte('M')
	if release {
		final = 'm'
	}
	return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", cb, x, y, final))
}

// encodeX10Mouse renders a legacy X10 mouse report: ESC [ M followed by three
// bytes, each the value plus 32. This format cannot represent a coordinate
// past 223 (255-32; xterm reserves byte 255 and up) — the caller must drop
// the event in that case, which is exactly why mode 1006/SGR exists.
func encodeX10Mouse(cb, x, y int) ([]byte, bool) {
	if x < 1 || y < 1 || x > 223 || y > 223 || cb < 0 || cb > 223 {
		return nil, false
	}
	return []byte{0x1b, '[', 'M', byte(32 + cb), byte(32 + x), byte(32 + y)}, true
}

// encodeMouseReport renders one mouse event bound for a pane's pty: SGR when
// the app asked for it (mode 1006), otherwise legacy X10. x,y are 0-based
// pane-local coordinates. cb is the event's button code before any
// X10-specific release override — X10 always reports a release as button 3,
// since (unlike SGR) it cannot say which button went up. ok is false when the
// event cannot be represented in X10 and must be dropped.
func encodeMouseReport(sgr bool, cb, x, y int, release bool) (data []byte, ok bool) {
	if sgr {
		return encodeSGRMouse(cb, x+1, y+1, release), true
	}
	if release {
		cb = 3
	}
	return encodeX10Mouse(cb, x+1, y+1)
}

// arrowSeq renders the cursor-key sequence xterm's alternateScroll feature
// substitutes for a wheel notch on the alternate screen when the app has not
// asked for mouse reporting: ESC [ A/B normally, or ESC O A/B when the app
// turned on application-cursor mode (DECCKM).
func arrowSeq(kind string, appCursor bool) []byte {
	dir := byte('A')
	if kind == protocol.MouseWheelDown {
		dir = 'B'
	}
	mid := byte('[')
	if appCursor {
		mid = 'O'
	}
	return []byte{0x1b, mid, dir}
}

// localCoords translates absolute content-area coordinates to coordinates
// local to rect, clamped to its bounds. Clamping keeps a drag that strays
// outside the pane it started on reporting a coordinate the app can make
// sense of, instead of a negative one or one past the pane's own size.
func localCoords(rect layout.Rect, x, y int) (int, int) {
	lx, ly := x-rect.X, y-rect.Y
	if lx < 0 {
		lx = 0
	} else if maxX := rect.W - 1; lx > maxX {
		lx = maxX
	}
	if ly < 0 {
		ly = 0
	} else if maxY := rect.H - 1; ly > maxY {
		ly = maxY
	}
	return lx, ly
}
