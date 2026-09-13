//go:build !windows

// Package e2e exercises the daemon and client together over a real pty, the
// same way an interactive terminal would drive ngmux.
package e2e

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/MauricioJC3/ng_mux/internal/client"
	"github.com/MauricioJC3/ng_mux/internal/ipc"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/server"
)

// execCmd sends a one-shot command line to the daemon and returns its reply.
func execCmd(t *testing.T, ep ipc.Endpoint, line string) string {
	t.Helper()
	conn, err := ipc.Dial(ep)
	if err != nil {
		t.Fatalf("dial for exec: %v", err)
	}
	defer conn.Close()
	pc := protocol.NewConn(conn)
	if err := pc.Write(protocol.Message{Type: protocol.TypeExec, Name: line}); err != nil {
		t.Fatalf("exec write: %v", err)
	}
	reply, err := pc.Read()
	if err != nil {
		t.Fatalf("exec read: %v", err)
	}
	return reply.Name
}

// killServer tells a running daemon to shut down.
func killServer(ep ipc.Endpoint) {
	conn, err := ipc.Dial(ep)
	if err != nil {
		return
	}
	pc := protocol.NewConn(conn)
	_ = pc.Write(protocol.Message{Type: protocol.TypeKillServer})
	_, _ = pc.Read()
	conn.Close()
}

// harness starts an in-process daemon and attaches one client through a pty.
type harness struct {
	t      *testing.T
	master *os.File
	ep     ipc.Endpoint
	srvErr chan error
	cliErr chan error
	mu     sync.Mutex
	buf    strings.Builder
	logs   lockedBuffer // the daemon's log output
}

// lockedBuffer collects the daemon log, which is written from many goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// logText returns everything the daemon has logged so far.
func (h *harness) logText() string {
	h.logs.mu.Lock()
	defer h.logs.mu.Unlock()
	return h.logs.buf.String()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// The daemon runs in-process here and starts panes with $SHELL. Force a
	// plain POSIX shell so a developer's interactive rc files (prompt wizards,
	// Nerd-Font checks, etc.) cannot take over the pane and break assertions.
	t.Setenv("SHELL", "/bin/sh")

	ep := ipc.Endpoint{Name: "e2e-" + strings.ReplaceAll(t.Name(), "/", "_")}
	ipc.Remove(ep)

	h := &harness{t: t, ep: ep, srvErr: make(chan error, 1), cliErr: make(chan error, 1)}

	ready := make(chan struct{})
	go func() {
		// server.Run blocks; signal readiness once the socket is dialable.
		go func() {
			for i := 0; i < 200; i++ {
				if c, err := ipc.Dial(ep); err == nil {
					c.Close()
					close(ready)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		h.srvErr <- server.Run(ep, 80, 24, log.New(&h.logs, "", log.LstdFlags))
	}()

	select {
	case <-ready:
	case err := <-h.srvErr:
		t.Fatalf("server exited before ready: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("server never became ready")
	}

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatalf("setsize: %v", err)
	}
	h.master = master

	go func() {
		b := make([]byte, 4096)
		for {
			n, err := master.Read(b)
			if n > 0 {
				h.mu.Lock()
				h.buf.Write(b[:n])
				h.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	go func() { h.cliErr <- client.Attach(ep, "", slave, slave) }()

	t.Cleanup(func() {
		killServer(ep)
		select {
		case <-h.cliErr:
		case <-time.After(2 * time.Second):
		}
		select {
		case <-h.srvErr:
		case <-time.After(2 * time.Second):
		}
		_ = master.Close()
		_ = slave.Close()
		ipc.Remove(ep)
	})

	return h
}

func (h *harness) send(s string) {
	h.t.Helper()
	if _, err := h.master.Write([]byte(s)); err != nil {
		h.t.Fatalf("write to pty: %v", err)
	}
}

func (h *harness) screen() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buf.String()
}

// reset drops everything received so far. Use it right before an action that
// triggers a full repaint (window/session switch, resize) so subsequent
// assertions see only freshly painted content.
func (h *harness) reset() {
	h.mu.Lock()
	h.buf.Reset()
	h.mu.Unlock()
}

func (h *harness) refute(unwanted string, settle time.Duration) {
	h.t.Helper()
	time.Sleep(settle)
	if strings.Contains(h.screen(), unwanted) {
		h.t.Fatalf("did not expect %q on screen; got:\n%q", unwanted, h.screen())
	}
}

// waitFor polls the accumulated terminal output until it contains want.
func (h *harness) waitFor(want string, timeout time.Duration) bool {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(h.screen(), want) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestEchoInPane(t *testing.T) {
	h := newHarness(t)
	// Give the shell a moment to start, then run a command.
	time.Sleep(300 * time.Millisecond)
	h.send("printf MARKER_ONE\r")
	if !h.waitFor("MARKER_ONE", 3*time.Second) {
		t.Fatalf("expected shell output in pane; got:\n%q", h.screen())
	}
}

func TestStatusBarShowsSession(t *testing.T) {
	h := newHarness(t)
	if !h.waitFor("[0]", 2*time.Second) {
		t.Fatalf("status bar never showed the session name; got:\n%q", h.screen())
	}
}

func TestSplitShowsBothPanes(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)
	h.send("printf PANE_AAA\r")
	if !h.waitFor("PANE_AAA", 3*time.Second) {
		t.Fatalf("first pane never produced output; got:\n%q", h.screen())
	}
	h.send("\x02%") // split left/right; focus moves to the new pane
	time.Sleep(500 * time.Millisecond)
	h.send("printf PANE_BBB\r")
	if !h.waitFor("PANE_BBB", 3*time.Second) {
		t.Fatalf("second pane never produced output; got:\n%q", h.screen())
	}
	// Both panes are visible at once.
	if !strings.Contains(h.screen(), "PANE_AAA") {
		t.Fatalf("first pane vanished after split; got:\n%q", h.screen())
	}
}

func TestNewWindowIsolatesContent(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)
	h.send("printf WIN_ZERO\r")
	if !h.waitFor("WIN_ZERO", 3*time.Second) {
		t.Fatalf("window 0 never produced output; got:\n%q", h.screen())
	}

	h.send("\x02c") // new window, becomes current
	time.Sleep(600 * time.Millisecond)
	h.reset()
	h.send("printf WIN_ONE\r")
	if !h.waitFor("WIN_ONE", 3*time.Second) {
		t.Fatalf("new window never produced output; got:\n%q", h.screen())
	}
	h.refute("WIN_ZERO", 300*time.Millisecond) // window 0's content is not shown here

	h.reset()
	h.send("\x02p") // back to window 0
	if !h.waitFor("WIN_ZERO", 3*time.Second) {
		t.Fatalf("returning to window 0 did not restore its content; got:\n%q", h.screen())
	}
	h.refute("WIN_ONE", 300*time.Millisecond)
}

func TestKillWindowReturnsToRemaining(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)
	h.send("printf HOME_MARK\r")
	if !h.waitFor("HOME_MARK", 3*time.Second) {
		t.Fatalf("window 0 never produced output; got:\n%q", h.screen())
	}
	h.send("\x02c") // second window
	time.Sleep(600 * time.Millisecond)
	h.reset()
	h.send("\x02&") // kill the second window -> back to window 0
	if !h.waitFor("HOME_MARK", 3*time.Second) {
		t.Fatalf("killing a window did not return to the remaining one; got:\n%q", h.screen())
	}
}

func TestKillLastWindowEndsSessionAndClient(t *testing.T) {
	h := newHarness(t)
	time.Sleep(400 * time.Millisecond)
	h.send("\x02&") // kill the only window -> session empties -> server stops
	select {
	case <-h.cliErr:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not exit after the last window was killed")
	}
	h.cliErr <- nil // let cleanup's receive proceed
}

func TestNamedSessionAppearsInList(t *testing.T) {
	h := newHarness(t) // default client on session "0"
	time.Sleep(200 * time.Millisecond)

	// A second client attaches to a new named session.
	conn, err := ipc.Dial(h.ep)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	pc := protocol.NewConn(conn)
	if err := pc.Write(protocol.Message{Type: protocol.TypeAttach, Name: "work", Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("attach write: %v", err)
	}
	defer conn.Close()
	time.Sleep(300 * time.Millisecond)

	// Ask a third, throwaway connection for the session list.
	lc, err := ipc.Dial(h.ep)
	if err != nil {
		t.Fatalf("dial list: %v", err)
	}
	defer lc.Close()
	lpc := protocol.NewConn(lc)
	if err := lpc.Write(protocol.Message{Type: protocol.TypeListReq}); err != nil {
		t.Fatalf("list write: %v", err)
	}
	reply, err := lpc.Read()
	if err != nil {
		t.Fatalf("list read: %v", err)
	}
	names := map[string]bool{}
	for _, s := range reply.Sessions {
		names[s.Name] = true
	}
	if !names["0"] || !names["work"] {
		t.Fatalf("expected sessions 0 and work, got %+v", reply.Sessions)
	}
}

func TestCopyModeShowsScrollback(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)

	// Print many numbered lines so the earliest scroll off the 24-row screen.
	h.send("i=1; while [ $i -le 60 ]; do printf 'SBK_%d\\n' $i; i=$((i+1)); done\r")
	if !h.waitFor("SBK_60", 4*time.Second) {
		t.Fatalf("bulk output never finished; got tail:\n%q", tail(h.screen(), 400))
	}
	// SBK_1 has long since scrolled past the live screen.
	h.reset()
	h.send("\x02[") // enter copy-mode
	time.Sleep(150 * time.Millisecond)
	h.send("g") // jump to the oldest line
	if !h.waitFor("SBK_1", 3*time.Second) {
		t.Fatalf("copy-mode did not reveal scrolled-off output; got:\n%q", h.screen())
	}
	if !h.waitFor("COPY", 2*time.Second) {
		t.Fatalf("status bar never showed the COPY indicator; got:\n%q", h.screen())
	}
	h.send("q") // leave copy-mode
}

func TestConfigOverridesPrefix(t *testing.T) {
	confDir := t.TempDir()
	confPath := filepath.Join(confDir, "ngmux.conf")
	if err := os.WriteFile(confPath, []byte("set prefix C-a\nset history-limit 500\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NGMUX_CONFIG", confPath)

	h := newHarness(t) // server + client both read NGMUX_CONFIG
	time.Sleep(300 * time.Millisecond)

	h.send("printf CFG_AAA\r")
	if !h.waitFor("CFG_AAA", 3*time.Second) {
		t.Fatalf("first pane never produced output; got:\n%q", h.screen())
	}
	h.send("\x01%") // C-a + % : split, using the configured prefix
	time.Sleep(500 * time.Millisecond)
	h.send("printf CFG_BBB\r")
	if !h.waitFor("CFG_BBB", 3*time.Second) {
		t.Fatalf("split via configured prefix did not create a working pane; got:\n%q", h.screen())
	}
	if !strings.Contains(h.screen(), "CFG_AAA") {
		t.Fatalf("original pane vanished; got:\n%q", h.screen())
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestExecSendKeysReachesPane(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)
	execCmd(t, h.ep, `send-keys -t 0 "printf EXEC_MARK" Enter`)
	if !h.waitFor("EXEC_MARK", 3*time.Second) {
		t.Fatalf("send-keys did not reach the pane; got:\n%q", h.screen())
	}
}

func TestExecRenameWindowShowsInStatus(t *testing.T) {
	h := newHarness(t)
	if !h.waitFor("[0]", 2*time.Second) {
		t.Fatalf("status never appeared")
	}
	execCmd(t, h.ep, "rename-window BUILDX")
	if !h.waitFor("BUILDX", 3*time.Second) {
		t.Fatalf("renamed window not shown in status bar; got:\n%q", h.screen())
	}
}

func TestExecRenameSessionReflectedInList(t *testing.T) {
	h := newHarness(t)
	time.Sleep(200 * time.Millisecond)
	execCmd(t, h.ep, "rename-session prod")
	out := execCmd(t, h.ep, "list-sessions")
	if !strings.Contains(out, "prod:") {
		t.Fatalf("list-sessions after rename = %q, want it to mention 'prod:'", out)
	}
}

func TestCommandPromptSplits(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)
	h.send("printf PROMPT_A\r")
	if !h.waitFor("PROMPT_A", 3*time.Second) {
		t.Fatalf("first pane never produced output")
	}
	h.send("\x02:") // prefix then ':' opens the command prompt
	time.Sleep(150 * time.Millisecond)
	h.send("split-window -h\r") // type the command and run it
	time.Sleep(500 * time.Millisecond)
	h.send("printf PROMPT_B\r")
	if !h.waitFor("PROMPT_B", 3*time.Second) {
		t.Fatalf("command-prompt split did not create a working pane; got:\n%q", h.screen())
	}
	if !strings.Contains(h.screen(), "PROMPT_A") {
		t.Fatalf("original pane vanished after prompt split")
	}
}

func TestSelectLayoutKeepsPanes(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)
	execCmd(t, h.ep, "split-window -h")
	time.Sleep(300 * time.Millisecond)
	execCmd(t, h.ep, "split-window -v")
	time.Sleep(300 * time.Millisecond)
	execCmd(t, h.ep, "select-layout even-horizontal")
	out := execCmd(t, h.ep, "list-windows")
	if !strings.Contains(out, "3 panes") {
		t.Fatalf("expected 3 panes after splits+layout; list-windows = %q", out)
	}
}

func TestMouseClickFocusesPane(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)
	execCmd(t, h.ep, "split-window -h") // focus is now on the right pane
	time.Sleep(400 * time.Millisecond)
	// SGR mouse press at column 3, row 3 (1-based) -> inside the LEFT pane.
	h.send("\x1b[<0;3;3M")
	h.send("\x1b[<0;3;3m")
	time.Sleep(200 * time.Millisecond)
	// Keystrokes now go to the left pane.
	h.send("printf LEFT_HIT\r")
	if !h.waitFor("LEFT_HIT", 3*time.Second) {
		t.Fatalf("click did not move focus to the left pane; got:\n%q", h.screen())
	}
}

func TestStatusBarPlusButtonAddsWindow(t *testing.T) {
	h := newHarness(t)
	if !h.waitFor("[+]", 2*time.Second) {
		t.Fatalf("status bar never showed the [+] button; got:\n%q", h.screen())
	}
	// Status row is the last row (24 in a 24-row terminal). " [0] 0:sh* " is
	// 11 cells, so "[+]" sits at 1-based columns 12-14.
	h.send("\x1b[<0;13;24M")
	h.send("\x1b[<0;13;24m")
	out := ""
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out = execCmd(t, h.ep, "list-windows")
		if strings.Count(out, "panes") >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("clicking [+] did not add a window; list-windows = %q", out)
}

func TestStatusBarClickSwitchesWindow(t *testing.T) {
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)
	h.send("printf STATUS_W0\r")
	if !h.waitFor("STATUS_W0", 3*time.Second) {
		t.Fatalf("window 0 never produced output")
	}
	h.send("\x02c") // new window, now on window 1
	time.Sleep(500 * time.Millisecond)
	h.reset()
	// Click window 0's entry in the status bar (1-based column 7 is inside "0:sh").
	h.send("\x1b[<0;7;24M")
	h.send("\x1b[<0;7;24m")
	if !h.waitFor("STATUS_W0", 3*time.Second) {
		t.Fatalf("clicking the window entry did not switch back to window 0; got:\n%q", h.screen())
	}
}

func TestDetachStopsClient(t *testing.T) {
	h := newHarness(t)
	time.Sleep(200 * time.Millisecond)
	h.send("\x02d")
	select {
	case err := <-h.cliErr:
		if err != nil {
			t.Fatalf("client returned error on detach: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not exit after detach")
	}
	// Re-fill cliErr so cleanup's receive does not block.
	h.cliErr <- nil
}
