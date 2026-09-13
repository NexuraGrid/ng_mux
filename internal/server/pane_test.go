package server

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

func TestPanePumpForwardsOutputThenSignalsExit(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)
	p := &pane{id: 1, pt: pt, vt: sc}

	exited := make(chan *pane, 1)
	go p.pump(func(ep *pane) { exited <- ep })

	pt.feed([]byte("hello world"))
	waitFor(t, func() bool { return sc.consumedBytes() == "hello world" }, time.Second)

	pt.Close()
	select {
	case got := <-exited:
		if got != p {
			t.Fatalf("onExit called with %v, want the pane itself", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pump did not call onExit after the pty closed")
	}
}

// TestPanePumpSurvivesEmulatorPanicError is the regression test for the crash
// this change fixes: a pane whose screen reports a recovered emulator panic
// (vterm.Term.Write wraps ErrEmulatorPanic instead of letting the panic climb
// out) must log it once, with its own pane id, and keep pumping — never take
// the shell, let alone the daemon, down with it.
func TestPanePumpSurvivesEmulatorPanicError(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)

	var mu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	p := &pane{id: 7, pt: pt, vt: sc, logf: logf}

	exited := make(chan *pane, 1)
	go p.pump(func(ep *pane) { exited <- ep })

	sc.setWriteErr(fmt.Errorf("boom: %w", vterm.ErrEmulatorPanic))
	pt.feed([]byte("trigger"))

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(logs) > 0
	}, time.Second)

	mu.Lock()
	got := append([]string(nil), logs...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected exactly one logged occurrence, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "pane 7") {
		t.Fatalf("log entry %q does not mention the pane id", got[0])
	}

	// pump must keep consuming pty output after the error.
	pt.feed([]byte("still alive"))
	waitFor(t, func() bool { return strings.Contains(sc.consumedBytes(), "still alive") }, time.Second)

	pt.Close()
	select {
	case got := <-exited:
		if got != p {
			t.Fatalf("onExit called with %v, want the pane itself", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pump did not call onExit after the pty closed")
	}
}

// TestPanePumpSplitsLargeWritesIntoBoundedChunks is the regression test for
// the lock-convoy fix: a single big burst (bigger than one pty read buffer's
// worth) must reach the screen as several Write calls, none larger than
// maxWriteChunk, so vterm.Term.mu is released between them instead of being
// held for the whole burst. The concatenation of every recorded chunk must
// still equal the original bytes byte-for-byte.
func TestPanePumpSplitsLargeWritesIntoBoundedChunks(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)
	p := &pane{id: 1, pt: pt, vt: sc}

	exited := make(chan *pane, 1)
	go p.pump(func(ep *pane) { exited <- ep })

	// Bigger than maxWriteChunk and not an exact multiple of it, so the last
	// chunk is a genuine remainder rather than landing on a boundary by luck.
	burst := make([]byte, 3*maxWriteChunk+777)
	for i := range burst {
		burst[i] = byte('a' + i%26)
	}
	pt.feed(burst)
	waitFor(t, func() bool { return sc.consumedBytes() == string(burst) }, time.Second)

	sizes := sc.recordedWriteSizes()
	if len(sizes) < 2 {
		t.Fatalf("expected the burst to be split into multiple Write calls, got %v", sizes)
	}
	total := 0
	for _, n := range sizes {
		if n > maxWriteChunk {
			t.Fatalf("Write call of %d bytes exceeds maxWriteChunk (%d): %v", n, maxWriteChunk, sizes)
		}
		total += n
	}
	if total != len(burst) {
		t.Fatalf("chunks total %d bytes, want %d", total, len(burst))
	}

	pt.Close()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("pump did not call onExit after the pty closed")
	}
}

// TestPanePumpMultiByteRuneAcrossChunkBoundary feeds pump a burst long enough
// that pump's own maxWriteChunk slicing cuts a multi-byte UTF-8 rune in half,
// using a real vterm.Term (not the fake) as the screen so the rune actually
// gets parsed by the emulator. If vterm.Term.Write did not carry the
// incomplete tail across calls, the rune would render as mojibake instead of
// itself.
func TestPanePumpMultiByteRuneAcrossChunkBoundary(t *testing.T) {
	pt := newFakePty(80, 24)
	scr := vterm.New(80, 24, nil)
	p := &pane{id: 1, pt: pt, vt: scr}

	exited := make(chan *pane, 1)
	go p.pump(func(ep *pane) { exited <- ep })

	glyph := []byte("世") // E4 B8 96, a 3-byte rune
	var buf bytes.Buffer
	// Pad with ASCII up to exactly one byte short of the chunk boundary, so
	// the glyph's first byte lands on the last byte of the first chunk and
	// the rest spills into the second.
	buf.Write(bytes.Repeat([]byte("x"), maxWriteChunk-1))
	buf.Write(glyph)
	buf.WriteByte(' ') // settle the cursor past the glyph before snapshotting
	data := buf.Bytes()

	pt.feed(data)
	hasGlyph := func(snap vterm.Snapshot) bool {
		for y := 0; y < snap.Rows; y++ {
			for x := 0; x < snap.Cols; x++ {
				if snap.At(x, y).Ch == '世' {
					return true
				}
			}
		}
		return false
	}
	waitFor(t, func() bool { return hasGlyph(scr.Snapshot()) }, time.Second)

	pt.Close()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("pump did not call onExit after the pty closed")
	}
}

// TestPanePumpMultiByteRuneAcrossTwoPtyReads is the other split point named
// in the task: the OS pty read boundary itself, independent of pump's
// chunking (both halves here are well under maxWriteChunk). This is the
// preexisting case vt10x's own Write already mishandled across separate
// calls (see vterm.incompleteRuneTail's doc comment); pump relies on
// vterm.Term.Write to carry the tail across the two pt.feed calls below,
// which map to two separate pt.Read calls inside pump.
func TestPanePumpMultiByteRuneAcrossTwoPtyReads(t *testing.T) {
	pt := newFakePty(80, 24)
	scr := vterm.New(80, 24, nil)
	p := &pane{id: 1, pt: pt, vt: scr}

	exited := make(chan *pane, 1)
	go p.pump(func(ep *pane) { exited <- ep })

	glyph := []byte("🙂") // F0 9F 99 82, a 4-byte rune
	pt.feed(glyph[:2])
	// Give pump a chance to consume the first read before the second arrives,
	// so this genuinely exercises two separate pt.Read calls rather than
	// coalescing into one.
	time.Sleep(20 * time.Millisecond)
	pt.feed(glyph[2:])
	pt.feed([]byte(" "))

	waitFor(t, func() bool {
		snap := scr.Snapshot()
		return snap.At(0, 0).Ch == '🙂'
	}, time.Second)

	pt.Close()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("pump did not call onExit after the pty closed")
	}
}

func TestPaneResizePropagatesToPtyAndScreen(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)
	p := &pane{id: 1, pt: pt, vt: sc}

	p.resize(40, 10)

	if c, r := pt.size(); c != 40 || r != 10 {
		t.Errorf("pty size = %dx%d, want 40x10", c, r)
	}
	if c, r := sc.size(); c != 40 || r != 10 {
		t.Errorf("screen size = %dx%d, want 40x10", c, r)
	}
}

func TestPaneResizeClampsCopyCursor(t *testing.T) {
	pt := newFakePty(80, 24)
	sc := newFakeScreen(80, 24)
	p := &pane{id: 1, pt: pt, vt: sc, copy: newCopyState(80, 24)}
	p.copy.cy = 23

	p.resize(80, 10)

	if p.copy.cy != 9 {
		t.Errorf("copy cursor y = %d, want it clamped to 9", p.copy.cy)
	}
	if p.copy.rows != 10 || p.copy.cols != 80 {
		t.Errorf("copy viewport = %dx%d, want 80x10", p.copy.cols, p.copy.rows)
	}
}
