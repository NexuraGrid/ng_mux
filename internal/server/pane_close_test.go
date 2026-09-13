package server

import (
	"testing"
	"time"
)

// TestExitedPaneClosesItsScreen checks that reaping a pane whose process ended
// also closes its emulator, which stops the emulator's reply goroutine.
func TestExitedPaneClosesItsScreen(t *testing.T) {
	ff := &fakeFleet{}
	sess, err := newSession("work", 80, 24,
		sessionOpts{historyLimit: 100, defaultShell: "/bin/fakesh", newPane: ff.factory()},
		func(string) {})
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	t.Cleanup(sess.shutdown)

	fp := ff.byID(1)
	fp.pt.Close()
	waitFor(t, fp.scr.isClosed, 2*time.Second)
}

func TestPaneCloseClosesScreen(t *testing.T) {
	sc := newFakeScreen(80, 24)
	p := &pane{id: 1, pt: newFakePty(80, 24), vt: sc}
	p.close()
	if !sc.isClosed() {
		t.Fatal("pane.close did not close its screen")
	}
}
