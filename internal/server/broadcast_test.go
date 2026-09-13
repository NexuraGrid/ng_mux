package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// blockingScreen is a minimal screen fake whose Write blocks (holding its own
// mutex) until the test releases it, while Dirty reads an atomic flag with no
// locking at all -- mirroring vterm.Term's contract after this change (Dirty
// is lock-free; see internal/vterm's own tests for that guarantee against the
// real implementation). It exists purely to prove session.dirty() and
// session.input() never wait on a pane's screen lock.
type blockingScreen struct {
	mu         sync.Mutex
	dirty      atomic.Bool
	release    chan struct{}
	cols, rows int
}

func newBlockingScreen(cols, rows int) *blockingScreen {
	return &blockingScreen{release: make(chan struct{}), cols: cols, rows: rows}
}

func (s *blockingScreen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty.Store(true)
	<-s.release
	return len(p), nil
}

func (s *blockingScreen) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cols, s.rows = cols, rows
}

func (s *blockingScreen) Snapshot() vterm.Snapshot {
	var sn vterm.Snapshot
	s.SnapshotInto(&sn)
	return sn
}

func (s *blockingScreen) SnapshotInto(dst *vterm.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty.Store(false)
	dst.Cols, dst.Rows = s.cols, s.rows
	dst.Cells = make([]vterm.Cell, s.cols*s.rows)
}

func (s *blockingScreen) ScrollbackView(offset, rows int) vterm.Snapshot {
	if rows < 1 {
		rows = 1
	}
	return vterm.Snapshot{Cols: s.cols, Rows: rows, Cells: make([]vterm.Cell, s.cols*rows)}
}

func (s *blockingScreen) HistoryLen() int       { return 0 }
func (s *blockingScreen) SetHistoryLimit(n int) {}
func (s *blockingScreen) Dirty() bool           { return s.dirty.Load() }

func TestSessionDirtyLifecycle(t *testing.T) {
	_, ff, sess := setupSession(t)

	if !sess.dirty() {
		t.Fatal("a new session should be dirty until its first frame")
	}
	sess.frame()
	if sess.dirty() {
		t.Fatal("session should be clean right after frame()")
	}

	ff.byID(1).scr.Write([]byte("x")) // pane produced output
	if !sess.dirty() {
		t.Fatal("pane output should mark the session dirty")
	}
	sess.frame()
	if sess.dirty() {
		t.Fatal("frame() should clear pane dirtiness")
	}

	sess.mu.Lock()
	sess.needsRepaint = true
	sess.mu.Unlock()
	if !sess.dirty() {
		t.Fatal("needsRepaint alone should count as dirty")
	}
}

// TestSessionDirtyDoesNotBlockOnBusyPaneScreen is the server-level regression
// test for the lock-convoy fix: with one pane's screen busy inside Write
// (holding its own lock, exactly as vterm.Term.mu is held while parsing a
// burst), session.dirty() must still return promptly, and so must
// session.input() -- both only need session.mu, which dirty() now holds for
// no longer than a lock-free Dirty() call per pane.
func TestSessionDirtyDoesNotBlockOnBusyPaneScreen(t *testing.T) {
	_, ff, sess := setupSession(t)

	scr := newBlockingScreen(80, 23)
	p := ff.byID(1).p

	sess.mu.Lock()
	p.vt = scr
	sess.mu.Unlock()

	writeDone := make(chan struct{})
	go func() {
		_, _ = scr.Write([]byte("x")) // blocks on scr.release, holding scr.mu
		close(writeDone)
	}()
	waitFor(t, func() bool { return scr.dirty.Load() }, time.Second)

	dirtyDone := make(chan bool, 1)
	go func() { dirtyDone <- sess.dirty() }()
	select {
	case got := <-dirtyDone:
		if !got {
			t.Error("dirty() = false while a pane's screen was busy, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("session.dirty() blocked on a busy pane screen")
	}

	inputDone := make(chan struct{})
	go func() {
		sess.input([]byte("a"))
		close(inputDone)
	}()
	select {
	case <-inputDone:
	case <-time.After(time.Second):
		t.Fatal("session.input() blocked on a busy pane screen")
	}

	close(scr.release)
	<-writeDone
}

func TestFrameBuffersPingPongBetweenTwo(t *testing.T) {
	_, _, sess := setupSession(t)
	f0 := sess.frame()
	f1 := sess.frame()
	f2 := sess.frame()
	f3 := sess.frame()

	if f0 == nil || f1 == nil {
		t.Fatal("frame() returned nil")
	}
	if f0 == f1 {
		t.Fatal("consecutive frames must use different buffers")
	}
	if f0 != f2 || f1 != f3 {
		t.Fatalf("frame buffers should cycle between exactly two (got %p,%p,%p,%p)", f0, f1, f2, f3)
	}
}
