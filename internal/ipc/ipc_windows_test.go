//go:build windows

package ipc

import (
	"net"
	"strings"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestPipeSDDLGrantsOnlyTheUser(t *testing.T) {
	sid, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sddl := pipeSDDL(sid)
	if sddl != "D:P(A;;GA;;;"+sid.String()+")" {
		t.Fatalf("pipeSDDL = %q", sddl)
	}
	if strings.Contains(sddl, ";WD)") || strings.Contains(sddl, ";AN)") {
		t.Fatalf("pipeSDDL grants Everyone or anonymous: %q", sddl)
	}
	if _, err := winio.SddlToSecurityDescriptor(sddl); err != nil {
		t.Fatalf("pipeSDDL is not valid SDDL: %v", err)
	}
}

func TestListenDialRoundTrip(t *testing.T) {
	ep := Endpoint{Name: "test-roundtrip-" + t.Name()}
	l, err := Listen(ep)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	holdAccepted(t, l)
	c, err := Dial(ep)
	if err != nil {
		t.Fatalf("Dial to our own server: %v", err)
	}
	c.Close()
}

func TestCheckServerOwnerRefusesAnotherSID(t *testing.T) {
	ep := Endpoint{Name: "test-owner-" + t.Name()}
	l, err := Listen(ep)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	holdAccepted(t, l)
	c, err := winio.DialPipe(pipeName(ep), nil)
	if err != nil {
		t.Fatalf("DialPipe: %v", err)
	}
	defer c.Close()
	// The test server runs as us, so pretend we are LocalSystem: the owner
	// check must then refuse it.
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkServerOwner(c, system); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Fatalf("checkServerOwner = %v, want another-user refusal", err)
	}
}

// holdAccepted serves one connection on l and keeps the server end open until
// the test ends. Closing it right after Accept would disconnect the pipe
// before the client's GetNamedPipeServerProcessId call, which then fails and
// makes the owner check refuse a server that is really ours.
func holdAccepted(t *testing.T, l net.Listener) {
	t.Helper()
	conns := make(chan net.Conn, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			conns <- c
		}
	}()
	t.Cleanup(func() {
		l.Close()
		select {
		case c := <-conns:
			c.Close()
		default:
		}
	})
}
