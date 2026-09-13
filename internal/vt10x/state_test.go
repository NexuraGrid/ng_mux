package vt10x

import "testing"

// TestTabStopsExtendAfterWidening pins the resize() bug where growing a
// terminal wrote the extended default tab stops into the just-discarded old
// tabs slice, bounded by its old (narrower) length. The loop body never ran,
// so every tab stop beyond the pre-resize width silently vanished and
// tab-aligned output (tables, `column -t`, tab-indented logs) jumped straight
// to the right margin once the cursor passed the old width.
func TestTabStopsExtendAfterWidening(t *testing.T) {
	term := New(WithSize(40, 24))
	term.Resize(100, 24)

	st := term.(*terminal)
	st.moveTo(0, 0)

	want := []int{8, 16, 24, 32, 40, 48, 56, 64, 72, 80, 88, 96, 99}
	for _, x := range want {
		if _, err := term.Write([]byte("\t")); err != nil {
			t.Fatalf("write tab: %v", err)
		}
		cur := term.Cursor()
		if cur.X != x {
			t.Fatalf("tab stop mismatch: got cursor.X=%d, want %d (stops so far: %v)", cur.X, x, want)
		}
	}
}

// TestTabStopsShrinkThenGrow guards the same extension path when a terminal
// first shrinks (dropping tab stops beyond the new width, per resize()'s
// truncating make([]bool, cols)) and then grows again: stops must resume from
// the last surviving one, not from stale state.
func TestTabStopsShrinkThenGrow(t *testing.T) {
	term := New(WithSize(80, 24))
	term.Resize(20, 24)
	term.Resize(50, 24)

	st := term.(*terminal)
	st.moveTo(0, 0)

	want := []int{8, 16, 24, 32, 40, 48, 49}
	for _, x := range want {
		if _, err := term.Write([]byte("\t")); err != nil {
			t.Fatalf("write tab: %v", err)
		}
		cur := term.Cursor()
		if cur.X != x {
			t.Fatalf("tab stop mismatch: got cursor.X=%d, want %d (stops so far: %v)", cur.X, x, want)
		}
	}
}
