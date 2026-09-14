package vt10x

import "testing"

// TestPrivateMarkedSGRLeavesAttributesAlone pins the bug where XTMODKEYS
// ("ESC[>4;2m", sent by Claude Code, fish 4 and neovim to request
// modifyOtherKeys) was dispatched as a plain SGR: 4 turned on underline and
// 2 faint, so every cell written afterwards showed a line under it.
func TestPrivateMarkedSGRLeavesAttributesAlone(t *testing.T) {
	for _, seq := range []string{"\x1b[>4;2m", "\x1b[>4m", "\x1b[?4m", "\x1b[<4m", "\x1b[=4m"} {
		term := New(WithSize(20, 5))
		st := term.(*terminal)
		term.Write([]byte("A" + seq + "B"))

		plain, marked := st.Cell(0, 0), st.Cell(1, 0)
		if marked.Mode != plain.Mode || marked.FG != plain.FG || marked.BG != plain.BG {
			t.Errorf("%q changed attributes: plain %+v, after %+v", seq, plain, marked)
		}
	}
}

// TestKittyKeyboardSequencesKeepCursor pins the other half of the same class:
// the kitty keyboard protocol ("ESC[>1u" push, "ESC[<u" pop, "ESC[?u" query,
// "ESC[=1;1u" set) ends in 'u', which was dispatched as DECRC and yanked the
// cursor back to the last saved position mid-draw.
func TestKittyKeyboardSequencesKeepCursor(t *testing.T) {
	for _, seq := range []string{"\x1b[>1u", "\x1b[<u", "\x1b[<1u", "\x1b[?u", "\x1b[=1;1u"} {
		term := New(WithSize(20, 5))
		term.Write([]byte("\x1b[s" + "abc" + seq))

		if cur := term.Cursor(); cur.X != 3 || cur.Y != 0 {
			t.Errorf("%q moved the cursor to (%d,%d), want (3,0)", seq, cur.X, cur.Y)
		}
	}
}

// TestPlainSGRAndCursorRestoreStillWork guards that ignoring marked
// sequences does not swallow the unmarked ones.
func TestPlainSGRAndCursorRestoreStillWork(t *testing.T) {
	term := New(WithSize(20, 5))
	st := term.(*terminal)
	term.Write([]byte("\x1b[4mU\x1b[0m\x1b[s" + "abc" + "\x1b[u"))

	if st.Cell(0, 0).Mode&attrUnderline == 0 {
		t.Errorf("ESC[4m no longer underlines")
	}
	if cur := term.Cursor(); cur.X != 1 {
		t.Errorf("ESC[u restored cursor to X=%d, want 1", cur.X)
	}
}
