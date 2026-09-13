package server

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/ipc"
	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// fakePty is an in-memory stand-in for *ptyx.Pane. feed() delivers bytes to
// Read as if the child wrote them; Write records bytes headed for the child;
// Close unblocks a pending Read with io.EOF.
type fakePty struct {
	in        chan []byte
	rbuf      []byte
	closed    chan struct{}
	closeOnce sync.Once

	mu         sync.Mutex
	written    bytes.Buffer
	cols, rows int
}

func newFakePty(cols, rows int) *fakePty {
	return &fakePty{in: make(chan []byte, 64), closed: make(chan struct{}), cols: cols, rows: rows}
}

func (f *fakePty) feed(b []byte) {
	select {
	case f.in <- append([]byte(nil), b...):
	case <-f.closed:
	}
}

func (f *fakePty) Read(p []byte) (int, error) {
	if len(f.rbuf) == 0 {
		select {
		case b := <-f.in:
			f.rbuf = b
		case <-f.closed:
			select {
			case b := <-f.in: // drain anything buffered before EOF
				f.rbuf = b
			default:
				return 0, io.EOF
			}
		}
	}
	n := copy(p, f.rbuf)
	f.rbuf = f.rbuf[n:]
	return n, nil
}

func (f *fakePty) Write(p []byte) (int, error) {
	select {
	case <-f.closed:
		return 0, io.ErrClosedPipe
	default:
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.Write(p)
}

func (f *fakePty) sentToChild() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.String()
}

func (f *fakePty) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cols, f.rows = cols, rows
	return nil
}

func (f *fakePty) size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cols, f.rows
}

func (f *fakePty) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

// fakeScreen is an in-memory stand-in for *vterm.Term. It records the bytes it
// consumed and its size, and mimics the dirty/Snapshot contract.
type fakeScreen struct {
	mu         sync.Mutex
	consumed   bytes.Buffer
	cols, rows int
	hist       int
	histLimit  int
	dirty      bool
	fillCh     rune  // when non-zero, snapshots/scrollback return this in every cell
	writeErr   error // when set, the next Write returns this error and clears it
	snapPanic  bool  // when set, the next SnapshotInto panics and clears it
}

func newFakeScreen(cols, rows int) *fakeScreen {
	return &fakeScreen{cols: cols, rows: rows, dirty: true}
}

func (s *fakeScreen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(p) > 0 {
		s.dirty = true
	}
	n, _ := s.consumed.Write(p)
	if s.writeErr != nil {
		err := s.writeErr
		s.writeErr = nil
		return n, err
	}
	return n, nil
}

// setWriteErr makes the next Write call return err instead of nil, to
// simulate vterm.Term.Write reporting a recovered emulator panic.
func (s *fakeScreen) setWriteErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeErr = err
}

func (s *fakeScreen) consumedBytes() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consumed.String()
}

func (s *fakeScreen) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cols, s.rows = cols, rows
	s.dirty = true
}

func (s *fakeScreen) size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

func (s *fakeScreen) Snapshot() vterm.Snapshot {
	var snap vterm.Snapshot
	s.SnapshotInto(&snap)
	return snap
}

// panicNextSnapshot makes the next SnapshotInto panic, simulating a bug while a
// frame is being built.
func (s *fakeScreen) panicNextSnapshot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapPanic = true
}

func (s *fakeScreen) SnapshotInto(dst *vterm.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapPanic {
		s.snapPanic = false
		panic("fakeScreen: injected snapshot panic")
	}
	s.dirty = false
	need := s.cols * s.rows
	if cap(dst.Cells) < need {
		dst.Cells = make([]vterm.Cell, need)
	} else {
		dst.Cells = dst.Cells[:need]
	}
	dst.Cols, dst.Rows = s.cols, s.rows
}

func (s *fakeScreen) ScrollbackView(offset, rows int) vterm.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rows < 1 {
		rows = 1
	}
	cells := make([]vterm.Cell, s.cols*rows)
	if s.fillCh != 0 {
		for i := range cells {
			cells[i] = vterm.Cell{Ch: s.fillCh, FG: vterm.ColorDefault, BG: vterm.ColorDefault}
		}
	}
	return vterm.Snapshot{Cols: s.cols, Rows: rows, Cells: cells}
}

func (s *fakeScreen) HistoryLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hist
}

func (s *fakeScreen) setHistoryLen(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hist = n
}

func (s *fakeScreen) SetHistoryLimit(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.histLimit = n
}

func (s *fakeScreen) Dirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// fakeFleet is a paneFactory that hands out fake panes and keeps a handle on
// each one so a test can inspect or drive it.
type fakeFleet struct {
	mu    sync.Mutex
	panes []*fakePane
}

type fakePane struct {
	id  layout.PaneID
	pt  *fakePty
	scr *fakeScreen
	p   *pane
}

func (ff *fakeFleet) factory() paneFactory {
	return func(id layout.PaneID, cols, rows int, shell string, hist int) (*pane, error) {
		pt := newFakePty(cols, rows)
		sc := newFakeScreen(cols, rows)
		sc.SetHistoryLimit(hist)
		p := &pane{id: id, pt: pt, vt: sc}
		ff.mu.Lock()
		ff.panes = append(ff.panes, &fakePane{id: id, pt: pt, scr: sc, p: p})
		ff.mu.Unlock()
		return p, nil
	}
}

func (ff *fakeFleet) count() int {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return len(ff.panes)
}

func (ff *fakeFleet) last() *fakePane {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if len(ff.panes) == 0 {
		return nil
	}
	return ff.panes[len(ff.panes)-1]
}

func (ff *fakeFleet) byID(id layout.PaneID) *fakePane {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	for _, fp := range ff.panes {
		if fp.id == id {
			return fp
		}
	}
	return nil
}

// newTestServer builds a Server backed by fake panes, with no listener.
func newTestServer(t testing.TB) (*Server, *fakeFleet) {
	t.Helper()
	ff := &fakeFleet{}
	srv := newServer(ipc.Endpoint{Name: "test"}, 80, 24, nil, sessionOpts{
		historyLimit: 100,
		defaultShell: "/bin/fakesh",
		statusFG:     0,
		statusBG:     7,
		setClipboard: true,
		newPane:      ff.factory(),
	})
	t.Cleanup(srv.shutdownAll)
	return srv, ff
}

// waitFor polls cond until it is true or the timeout elapses.
func waitFor(t testing.TB, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
