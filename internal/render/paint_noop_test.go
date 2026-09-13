package render

import (
	"strings"
	"testing"
)

func TestPaintSkipsUnchangedFrames(t *testing.T) {
	base := func() *Frame {
		f := NewFrame(10, 3)
		f.Cells[0].Ch = 'a'
		f.CurX, f.CurY, f.CurVisible = 1, 0, true
		return f
	}

	tests := []struct {
		name     string
		mutate   func(f *Frame)
		wantNil  bool
		contains string
	}{
		{name: "identical frame", mutate: func(*Frame) {}, wantNil: true},
		{name: "cursor moved", mutate: func(f *Frame) { f.CurX = 5 }, contains: "\x1b[1;6H"},
		{name: "cursor hidden", mutate: func(f *Frame) { f.CurVisible = false }},
		{name: "hidden cursor moved", mutate: func(f *Frame) { f.CurVisible = false; f.CurX = 7 }},
		{name: "cell changed", mutate: func(f *Frame) { f.Cells[1].Ch = 'b' }, contains: "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev, next := base(), base()
			tt.mutate(next)
			out := Paint(prev, next)
			if tt.wantNil {
				if out != nil {
					t.Fatalf("Paint = %q, want nil for an unchanged frame", out)
				}
				return
			}
			if len(out) == 0 {
				t.Fatal("Paint returned nothing for a visible change")
			}
			if tt.contains != "" && !strings.Contains(string(out), tt.contains) {
				t.Fatalf("Paint = %q, want it to contain %q", out, tt.contains)
			}
		})
	}

	// Once the cursor is hidden on both sides, moving it is invisible.
	prev, next := base(), base()
	prev.CurVisible, next.CurVisible = false, false
	next.CurX = 8
	if out := Paint(prev, next); out != nil {
		t.Fatalf("moving a hidden cursor painted %q, want nil", out)
	}
}
