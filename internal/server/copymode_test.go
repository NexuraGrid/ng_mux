package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// --- copyState.sync: the anchoring arithmetic itself ---

func TestCopyStateSyncKeepsOffsetAnchored(t *testing.T) {
	tests := []struct {
		name        string
		startOffset int
		seen        uint64
		pushedTotal uint64 // ScrolledTotal() as of this sync call
		histLen     int
		wantOffset  int
	}{
		{
			name:        "scrolled into history, more output arrives: offset tracks by the same delta",
			startOffset: 10, seen: 100, pushedTotal: 105, histLen: 200,
			wantOffset: 15,
		},
		{
			name:        "offset zero freezes the view instead of tracking live output",
			startOffset: 0, seen: 100, pushedTotal: 107, histLen: 200,
			wantOffset: 7,
		},
		{
			name:        "clamps at HistoryLen when the ring evicts lines being viewed",
			startOffset: 45, seen: 100, pushedTotal: 120, histLen: 50,
			wantOffset: 50,
		},
		{
			name:        "no lines pushed: no-op",
			startOffset: 12, seen: 100, pushedTotal: 100, histLen: 200,
			wantOffset: 12,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := newCopyState(80, 24)
			cs.seen = tt.seen
			cs.offset = tt.startOffset

			cs.sync(tt.pushedTotal, tt.histLen)

			if cs.offset != tt.wantOffset {
				t.Fatalf("offset = %d, want %d", cs.offset, tt.wantOffset)
			}
			if cs.seen != tt.pushedTotal {
				t.Fatalf("seen = %d, want %d (sync must record the new baseline)", cs.seen, tt.pushedTotal)
			}
		})
	}
}

func TestCopyStateSyncIsIdempotentBetweenPushes(t *testing.T) {
	cs := newCopyState(80, 24)
	cs.seen = 100
	cs.offset = 5

	cs.sync(110, 200) // one push of 10
	if cs.offset != 15 {
		t.Fatalf("offset after first sync = %d, want 15", cs.offset)
	}
	cs.sync(110, 200) // called again with nothing new: must not double-count
	if cs.offset != 15 {
		t.Fatalf("offset after redundant sync = %d, want 15 (unchanged)", cs.offset)
	}
}

// --- integration: wheel-down to the bottom still exits copy-mode after sync ---

func TestWheelDownToBottomExitsCopyModeAfterAnchoring(t *testing.T) {
	_, ff, sess := setupSession(t)
	id := activePane(sess)
	scr := ff.byID(id).scr
	scr.setHistoryLen(20)
	rect := activeRect(sess)

	sess.mouse(protocol.MouseWheelUp, rect.X, rect.Y, 0) // enter copy-mode, offset -> wheelStep (3)
	if !paneCopyActive(sess, id) {
		t.Fatal("wheel-up should have entered copy-mode")
	}

	// Output keeps arriving while the user is scrolled back.
	scr.setScrolledTotal(2)
	scr.setHistoryLen(22)

	// First wheel-down: sync adds the 2 pending lines (offset 3 -> 5), then
	// wheelStep (3) is subtracted, landing at 2 (still above the bottom).
	sess.mouse(protocol.MouseWheelDown, rect.X, rect.Y, 0)
	if !paneCopyActive(sess, id) {
		t.Fatal("should still be in copy-mode: offset should be 2, not yet at the bottom")
	}

	// Second wheel-down: no new lines since the last sync, so offset just
	// drops by wheelStep (2 -> -1) and exits.
	sess.mouse(protocol.MouseWheelDown, rect.X, rect.Y, 0)
	if paneCopyActive(sess, id) {
		t.Fatal("should have exited copy-mode once scrolled to the bottom")
	}
}

// --- selection stays on the same text across a sync, against a real emulator ---

func snapRow(s vterm.Snapshot, y int) string {
	var b strings.Builder
	for x := 0; x < s.Cols; x++ {
		b.WriteRune(s.At(x, y).Ch)
	}
	return strings.TrimRight(b.String(), " ")
}

// TestAnchoredCopyModeKeepsSelectionOnSameText drives a real vterm.Term (not a
// fake) through pane.syncCopy to prove the anchoring mechanism keeps a
// scrolled-back view (and so a selection built from it) pointed at the same
// absolute lines while more output is written, rather than sliding as offset
// drifts relative to a growing live bottom.
func TestAnchoredCopyModeKeepsSelectionOnSameText(t *testing.T) {
	vt := vterm.New(20, 4, nil)
	vt.SetHistoryLimit(100)
	for i := 1; i <= 10; i++ {
		vt.Write([]byte(fmt.Sprintf("line%02d\r\n", i)))
	}

	p := &pane{vt: vt}
	p.copy = newCopyState(20, 4)
	p.copy.seen = vt.ScrolledTotal()
	p.copy.offset = vt.HistoryLen() // scrolled all the way back
	p.copy.cy = 0

	before := vt.ScrollbackView(p.copy.offset, p.copy.rows)
	rowBefore := snapRow(before, p.copy.cy)
	if rowBefore == "" {
		t.Fatal("setup produced an empty row; test is not exercising real content")
	}

	// A worker keeps printing while the user is looking at history.
	for i := 11; i <= 13; i++ {
		vt.Write([]byte(fmt.Sprintf("line%02d\r\n", i)))
	}

	p.syncCopy()

	after := vt.ScrollbackView(p.copy.offset, p.copy.rows)
	rowAfter := snapRow(after, p.copy.cy)

	if rowBefore != rowAfter {
		t.Fatalf("row at cursor drifted from %q to %q after output arrived; anchoring failed", rowBefore, rowAfter)
	}
}
