package vterm

import "testing"

// TestInputModesReflectsEnabledMouseAndSGRModes drives a real emulator (not a
// fake) through the same DEC private-mode escapes an app like htop or vim
// sends, and checks InputModes reports them back correctly. This is the
// contract internal/server's mouse routing relies on to decide whether to
// forward a mouse event to the app instead of driving copy-mode.
func TestInputModesReflectsEnabledMouseAndSGRModes(t *testing.T) {
	term := New(80, 24, nil)

	if m := term.InputModes(); m.MouseButton || m.MouseSGR {
		t.Fatalf("fresh terminal InputModes = %+v, want no mouse modes enabled", m)
	}

	term.Write([]byte("\x1b[?1000h\x1b[?1006h"))

	m := term.InputModes()
	if !m.MouseButton {
		t.Error("MouseButton = false after ESC[?1000h, want true")
	}
	if !m.MouseSGR {
		t.Error("MouseSGR = false after ESC[?1006h, want true")
	}
	if m.MouseX10 || m.MouseMotion || m.MouseAny {
		t.Errorf("unexpected mouse mode set alongside Button: %+v", m)
	}

	term.Write([]byte("\x1b[?1000l"))
	if m := term.InputModes(); m.MouseButton {
		t.Error("MouseButton = true after ESC[?1000l, want false")
	}
}

// TestInputModesReflectsAltScreenAndAppCursor covers the other two modes the
// server's mouse routing reads: alternate-screen detection (for the
// alt-screen wheel-to-arrow-keys translation) and application-cursor mode
// (which changes the arrow-key sequence it sends).
func TestInputModesReflectsAltScreenAndAppCursor(t *testing.T) {
	term := New(80, 24, nil)

	term.Write([]byte("\x1b[?1049h")) // enter alternate screen (vim, less, htop, ...)
	if m := term.InputModes(); !m.AltScreen {
		t.Error("AltScreen = false after entering the alternate screen, want true")
	}

	term.Write([]byte("\x1b[?1h")) // DECCKM: application cursor keys
	if m := term.InputModes(); !m.AppCursor {
		t.Error("AppCursor = false after ESC[?1h, want true")
	}

	term.Write([]byte("\x1b[?1049l")) // leave the alternate screen
	if m := term.InputModes(); m.AltScreen {
		t.Error("AltScreen = true after leaving the alternate screen, want false")
	}
}
