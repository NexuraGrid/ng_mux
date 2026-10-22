//go:build windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// pipeName builds a per-user named pipe path. Windows named pipes are already
// namespaced per machine; including the username keeps two users on the same
// box from colliding.
//
// The name is predictable, so another local user can create it first (pipe
// squatting). Listen then fails, since winio creates the first instance with
// FILE_CREATE, and Dial refuses a pipe whose server runs as someone else.
func pipeName(e Endpoint) string {
	user := os.Getenv("USERNAME")
	if user == "" {
		user = "user"
	}
	user = strings.NewReplacer("\\", "_", "/", "_", " ", "_").Replace(user)
	return fmt.Sprintf(`\\.\pipe\ngmux-%s-%s`, user, e.Name)
}

// currentUserSID returns the SID of the user this process runs as.
func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("ipc: current user SID: %w", err)
	}
	return tu.User.Sid, nil
}

// pipeSDDL grants full access to sid only: a protected DACL with no
// inherited entries, so neither Everyone nor anonymous can open the pipe (the
// default DACL gives them read access).
func pipeSDDL(sid *windows.SID) string {
	return "D:P(A;;GA;;;" + sid.String() + ")"
}

// Listen creates the server pipe.
func Listen(e Endpoint) (net.Listener, error) {
	name := pipeName(e)
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	// A successful dial means another server owns this endpoint.
	if c, err := winio.DialPipe(name, durPtr(200*time.Millisecond)); err == nil {
		ownErr := checkServerOwner(c, sid)
		c.Close()
		if ownErr != nil {
			return nil, ownErr
		}
		return nil, fmt.Errorf("ipc: server already running at %s", name)
	}
	cfg := &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL(sid),
		MessageMode:        false,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	}
	l, err := winio.ListenPipe(name, cfg)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen %s: %w", name, err)
	}
	return l, nil
}

// Dial connects to a running server, returning ErrNoServer when the pipe does
// not exist yet. It refuses a pipe served by another user, which would
// otherwise receive every keystroke typed into the session.
func Dial(e Endpoint) (net.Conn, error) {
	name := pipeName(e)
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, name)
	if err != nil {
		return nil, ErrNoServer
	}
	if err := checkServerOwner(c, sid); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// checkServerOwner verifies that the process serving pipe connection c runs
// as want. Anything it cannot verify (no handle, process not openable) counts
// as foreign: failing closed is the point.
func checkServerOwner(c net.Conn, want *windows.SID) error {
	refuse := func(why string) error {
		return fmt.Errorf("ipc: refusing pipe %s: %s", c.RemoteAddr(), why)
	}
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return refuse("no pipe handle to verify the server")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid); err != nil {
		return refuse(fmt.Sprintf("server process: %v", err))
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return refuse(fmt.Sprintf("server process %d is not ours: %v", pid, err))
	}
	defer windows.CloseHandle(proc)
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return refuse(fmt.Sprintf("server process %d is not ours: %v", pid, err))
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return refuse(fmt.Sprintf("server process %d owner: %v", pid, err))
	}
	if !tu.User.Sid.Equals(want) {
		return refuse(fmt.Sprintf("server process %d runs as another user (%s)", pid, tu.User.Sid))
	}
	return nil
}

// Remove is a no-op on Windows: a named pipe disappears when its last handle
// closes, so there is no file to clean up.
func Remove(e Endpoint) {}

func durPtr(d time.Duration) *time.Duration { return &d }
