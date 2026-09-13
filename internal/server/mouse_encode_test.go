package server

import (
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

func TestEncodeMouseReportSGR(t *testing.T) {
	tests := []struct {
		name        string
		cb, x, y    int
		release     bool
		wantPattern string
	}{
		{"press left", 0, 0, 0, false, "\x1b[<0;1;1M"},
		{"press right at offset", 2, 9, 4, false, "\x1b[<2;10;5M"},
		{"release", 0, 0, 0, true, "\x1b[<0;1;1m"},
		{"drag with button held", 32, 3, 3, false, "\x1b[<32;4;4M"},
		{"wheel up", 64, 0, 0, false, "\x1b[<64;1;1M"},
		{"wheel down", 65, 0, 0, false, "\x1b[<65;1;1M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, ok := encodeMouseReport(true, tt.cb, tt.x, tt.y, tt.release)
			if !ok {
				t.Fatalf("encodeMouseReport() ok = false, want true")
			}
			if got := string(data); got != tt.wantPattern {
				t.Fatalf("encodeMouseReport() = %q, want %q", got, tt.wantPattern)
			}
		})
	}
}

func TestEncodeMouseReportX10(t *testing.T) {
	tests := []struct {
		name     string
		cb, x, y int
		release  bool
		want     []byte
	}{
		{"press left", 0, 0, 0, false, []byte{0x1b, '[', 'M', 32, 33, 33}},
		{"press right", 2, 4, 4, false, []byte{0x1b, '[', 'M', 34, 37, 37}},
		// X10 has no per-button release: always reports button 3.
		{"release always button 3", 1, 0, 0, true, []byte{0x1b, '[', 'M', 35, 33, 33}},
		{"wheel up", 64, 0, 0, false, []byte{0x1b, '[', 'M', 96, 33, 33}},
		{"wheel down", 65, 0, 0, false, []byte{0x1b, '[', 'M', 97, 33, 33}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, ok := encodeMouseReport(false, tt.cb, tt.x, tt.y, tt.release)
			if !ok {
				t.Fatalf("encodeMouseReport() ok = false, want true")
			}
			if string(data) != string(tt.want) {
				t.Fatalf("encodeMouseReport() = %v, want %v", data, tt.want)
			}
		})
	}
}

func TestEncodeMouseReportX10DropsCoordinateOverLimit(t *testing.T) {
	tests := []struct {
		name string
		x, y int
	}{
		{"x too large", 224, 0},
		{"y too large", 0, 224},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := encodeMouseReport(false, 0, tt.x, tt.y, false); ok {
				t.Fatalf("encodeMouseReport() ok = true, want false (coordinate %d,%d exceeds X10's 223 limit)", tt.x, tt.y)
			}
		})
	}
}

func TestEncodeMouseReportSGRHasNoCoordinateLimit(t *testing.T) {
	// SGR mode is exactly why 1006 exists: it can encode coordinates X10 cannot.
	if _, ok := encodeMouseReport(true, 0, 500, 500, false); !ok {
		t.Fatal("encodeMouseReport(sgr=true) ok = false for a large coordinate, want true")
	}
}

func TestArrowSeq(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		appCursor bool
		want      string
	}{
		{"up normal", protocol.MouseWheelUp, false, "\x1b[A"},
		{"down normal", protocol.MouseWheelDown, false, "\x1b[B"},
		{"up app-cursor", protocol.MouseWheelUp, true, "\x1bOA"},
		{"down app-cursor", protocol.MouseWheelDown, true, "\x1bOB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(arrowSeq(tt.kind, tt.appCursor)); got != tt.want {
				t.Fatalf("arrowSeq() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForwardKindAllowed(t *testing.T) {
	tests := []struct {
		name  string
		modes vterm.InputModes
		kind  string
		want  bool
	}{
		{"X10 press", vterm.InputModes{MouseX10: true}, protocol.MousePress, true},
		{"X10 wheel", vterm.InputModes{MouseX10: true}, protocol.MouseWheelUp, true},
		{"X10 release not reported", vterm.InputModes{MouseX10: true}, protocol.MouseRelease, false},
		{"X10 drag not reported", vterm.InputModes{MouseX10: true}, protocol.MouseDrag, false},

		{"Button press", vterm.InputModes{MouseButton: true}, protocol.MousePress, true},
		{"Button release", vterm.InputModes{MouseButton: true}, protocol.MouseRelease, true},
		{"Button drag not reported", vterm.InputModes{MouseButton: true}, protocol.MouseDrag, false},

		{"Motion drag reported", vterm.InputModes{MouseMotion: true}, protocol.MouseDrag, true},
		{"Motion release reported", vterm.InputModes{MouseMotion: true}, protocol.MouseRelease, true},

		{"Any drag reported", vterm.InputModes{MouseAny: true}, protocol.MouseDrag, true},

		{"no mode, nothing forwarded", vterm.InputModes{}, protocol.MousePress, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := forwardKindAllowed(tt.modes, tt.kind); got != tt.want {
				t.Fatalf("forwardKindAllowed(%+v, %q) = %v, want %v", tt.modes, tt.kind, got, tt.want)
			}
		})
	}
}

func TestLocalCoordsClampsToRect(t *testing.T) {
	rect := layout.Rect{X: 10, Y: 5, W: 20, H: 8}
	tests := []struct {
		name  string
		x, y  int
		wantX int
		wantY int
	}{
		{"inside", 15, 8, 5, 3},
		{"at origin", 10, 5, 0, 0},
		{"before rect clamps to 0", 0, 0, 0, 0},
		{"past right/bottom clamps to max", 100, 100, 19, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gx, gy := localCoords(rect, tt.x, tt.y)
			if gx != tt.wantX || gy != tt.wantY {
				t.Fatalf("localCoords(%v, %d, %d) = (%d,%d), want (%d,%d)", rect, tt.x, tt.y, gx, gy, tt.wantX, tt.wantY)
			}
		})
	}
}
