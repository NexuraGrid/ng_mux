package server

import (
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// nopRWC is a do-nothing transport for building a *client in tests that only
// exercise server-side bookkeeping (session reassignment), never the wire.
type nopRWC struct{}

func (nopRWC) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopRWC) Write(p []byte) (int, error) { return len(p), nil }
func (nopRWC) Close() error                { return nil }

func testClient(session string) *client {
	return newClient(protocol.NewConn(nopRWC{}), session)
}

// TestSessionEmptiedDetachesItsClients: when the session a client is viewing
// empties, that client is detached (sent a bye) instead of being moved into
// another session, where it could kill something it never meant to touch.
// Clients on other sessions are left alone.
func TestSessionEmptiedDetachesItsClients(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, n := range []string{"a", "b"} {
		if _, err := srv.getOrCreateSession(n); err != nil {
			t.Fatalf("create %q: %v", n, err)
		}
	}
	onB, peerB := pipeClient(t, "b")
	onA, peerA := pipeClient(t, "a")
	srv.addClient(onB)
	srv.addClient(onA)
	colB, colA := collect(peerB, 0), collect(peerA, 0)

	srv.sessionEmptied("b")

	waitFor(t, func() bool { return colB.count(protocol.TypeBye) == 1 }, 2*time.Second)
	if got := onB.session(); got != "" {
		t.Fatalf("detached client still points at session %q", got)
	}
	if got := onA.session(); got != "a" {
		t.Fatalf("client on a surviving session moved to %q", got)
	}
	if colA.count(protocol.TypeBye) != 0 {
		t.Fatal("client on a surviving session was detached")
	}
	if srv.sessionByName("a") == nil {
		t.Fatal("surviving session was removed")
	}
}

// TestSessionEmptiedNeverMovesAClient is the concurrent case: several sessions
// emptying at once detach every one of their clients and never hand any of
// them another session. Run with -race.
func TestSessionEmptiedNeverMovesAClient(t *testing.T) {
	srv, _ := newTestServer(t)
	const n = 8
	names := make([]string, n)
	clients := make([]*client, n)
	for i := range names {
		names[i] = fmt.Sprintf("s%d", i)
		if _, err := srv.getOrCreateSession(names[i]); err != nil {
			t.Fatalf("create %q: %v", names[i], err)
		}
		clients[i] = testClient(names[i])
		srv.addClient(clients[i])
	}

	// Empty every session but the last, all at once.
	var wg sync.WaitGroup
	for i := 0; i < n-1; i++ {
		wg.Add(1)
		go func(name string) { defer wg.Done(); srv.sessionEmptied(name) }(names[i])
	}
	wg.Wait()

	for i, c := range clients[:n-1] {
		if got := c.session(); got != "" {
			t.Fatalf("client %d was moved to %q instead of detached", i, got)
		}
	}
	if got := clients[n-1].session(); got != names[n-1] {
		t.Fatalf("client on the surviving session moved to %q", got)
	}
}
