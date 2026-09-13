// Command ngmux is a small, cross-platform terminal multiplexer: a background
// daemon owns the sessions, windows and panes, thin client processes attach.
//
// Usage:
//
//	ngmux [new] [-s name]    start the server if needed, then attach
//	ngmux attach|a [-t name] attach to an already-running server
//	ngmux ls                 list sessions
//	ngmux kill-session -t n  kill one session
//	ngmux kill-server        stop the server
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/client"
	"github.com/MauricioJC3/ng_mux/internal/ipc"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/ptyx"
	"github.com/MauricioJC3/ng_mux/internal/server"
)

// version is the build version. Release builds set it via
// -ldflags "-X main.version=vX.Y.Z"; a plain `go build` leaves it "dev" and
// versionString falls back to the module version recorded by the Go toolchain.
var version = "dev"

func main() {
	args := os.Args[1:]
	cmd := "new"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	ep := ipc.DefaultEndpoint

	var err error
	switch cmd {
	case "new", "":
		err = cmdNew(ep, args)
	case "attach", "a":
		err = cmdAttach(ep, args)
	case "ls", "list-sessions":
		err = cmdList(ep)
	case "kill-session":
		err = cmdKillSession(ep, args)
	case "kill-server":
		err = cmdKillServer(ep)
	case "__server":
		// Internal: this process IS the daemon. Not for direct use.
		name := "default"
		if len(args) > 0 {
			name = args[0]
		}
		err = runDaemon(ipc.Endpoint{Name: name})
	case "help", "-h", "--help":
		usage(os.Stdout)
	case "version", "-v", "--version":
		fmt.Println("ngmux " + versionString())
	case "update", "upgrade":
		err = selfUpdate(os.Stdout, hasFlag(args, "--force"))
	case "run":
		// `ngmux run <command line...>` — explicit form.
		err = client.Exec(ep, strings.Join(args, " "))
	default:
		// Anything else is treated as a command line for the running server,
		// tmux-style: `ngmux new-window`, `ngmux send-keys -t 0 "ls" Enter`.
		err = client.Exec(ep, strings.Join(os.Args[1:], " "))
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "ngmux: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `ngmux - cross-platform terminal multiplexer

  ngmux [new] [-s name]      start the server if needed, then attach
  ngmux attach|a [-t name]   attach to a running server
  ngmux ls                   list sessions
  ngmux kill-session -t name kill one session
  ngmux kill-server          stop the server
  ngmux update               download and install the latest release
  ngmux version              print the version
  ngmux <command> [args...]  run a command on the running server, e.g.
                             ngmux new-window
                             ngmux new-session -s logs
                             ngmux send-keys -t 0 "ls -la" Enter
                             ngmux select-layout tiled
                             ngmux rename-window build

sessions:
  ngmux new -s NAME          create a session and attach to it
  ngmux attach -t NAME       re-attach to a running session
  ngmux ls                   list sessions (name, windows, panes)
  Ctrl-b m                   show this session cheat-sheet while attached

prefix key: Ctrl-b
  "  split top/bottom      %  split left/right       x  kill pane
  o  next pane             ;  previous pane          arrows  focus pane
  H/J/K/L  resize pane     d  detach                 :  command prompt
  c  new window            n / p  next / prev window   0-9  select window
  &  kill window           ( / )  prev / next session   m  session help
  [  copy-mode             ]  paste
`)
}

// flags parses a -s/-t "session name" option (both spellings mean the same
// thing) out of args and returns the name (may be empty).
func sessionFlag(args []string) (string, error) {
	fs := flag.NewFlagSet("ngmux", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	s := fs.String("s", "", "session name")
	t := fs.String("t", "", "target session name")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *s != "" {
		return *s, nil
	}
	return *t, nil
}

// nestGuardEnv is exported into every pane shell by the daemon. Its presence
// means "this process is already running inside an attached ngmux client".
const nestGuardEnv = "NGMUX"

// refuseIfNested blocks a second client from being attached from inside a pane.
// Two clients on the same terminal fight over the prefix key and input, and the
// original tmux/screen fix is the same: refuse unless the user clears the
// marker variable to force it.
func refuseIfNested() error {
	if os.Getenv(nestGuardEnv) == "" {
		return nil
	}
	return fmt.Errorf("already inside ngmux — nested clients fight over the prefix key and input; " +
		"detach first with Ctrl-b d, or unset " + nestGuardEnv + " to force")
}

// cmdNew attaches, starting the daemon first if nothing is listening.
func cmdNew(ep ipc.Endpoint, args []string) error {
	if err := refuseIfNested(); err != nil {
		return err
	}
	if err := ptyx.CheckAvailable(); err != nil {
		return err
	}
	name, err := sessionFlag(args)
	if err != nil {
		return err
	}
	if _, err := ipc.Dial(ep); err != nil {
		if err := startDaemon(ep); err != nil {
			return err
		}
	}
	return client.Attach(ep, name, os.Stdin, os.Stdout)
}

func cmdAttach(ep ipc.Endpoint, args []string) error {
	if err := refuseIfNested(); err != nil {
		return err
	}
	if err := ptyx.CheckAvailable(); err != nil {
		return err
	}
	name, err := sessionFlag(args)
	if err != nil {
		return err
	}
	if _, err := ipc.Dial(ep); err != nil {
		return fmt.Errorf("no server running (use `ngmux new`)")
	}
	return client.Attach(ep, name, os.Stdin, os.Stdout)
}

func cmdList(ep ipc.Endpoint) error {
	conn, err := ipc.Dial(ep)
	if err != nil {
		return fmt.Errorf("no server running")
	}
	pc := protocol.NewConn(conn)
	if err := pc.Write(protocol.Message{Type: protocol.TypeListReq}); err != nil {
		return err
	}
	reply, err := pc.Read()
	if err != nil {
		return err
	}
	if len(reply.Sessions) == 0 {
		fmt.Println("(no sessions)")
		return nil
	}
	for _, s := range reply.Sessions {
		mark := ""
		if s.Attached {
			mark = " (attached)"
		}
		fmt.Printf("%s: %d window(s), %d pane(s)%s\n", s.Name, s.Windows, s.Panes, mark)
	}
	return nil
}

func cmdKillSession(ep ipc.Endpoint, args []string) error {
	name, err := sessionFlag(args)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("kill-session needs -t <name>")
	}
	conn, err := ipc.Dial(ep)
	if err != nil {
		return fmt.Errorf("no server running")
	}
	pc := protocol.NewConn(conn)
	if err := pc.Write(protocol.Message{Type: protocol.TypeKillSession, Name: name}); err != nil {
		return err
	}
	_, _ = pc.Read()
	fmt.Printf("killed session %q\n", name)
	return nil
}

func cmdKillServer(ep ipc.Endpoint) error {
	conn, err := ipc.Dial(ep)
	if err != nil {
		return fmt.Errorf("no server running")
	}
	pc := protocol.NewConn(conn)
	if err := pc.Write(protocol.Message{Type: protocol.TypeKillServer}); err != nil {
		return err
	}
	_, _ = pc.Read() // wait for Bye
	fmt.Println("server stopped")
	return nil
}

// startDaemon spawns a detached copy of this binary running `__server` and
// waits for its socket to come up.
func startDaemon(ep ipc.Endpoint) error {
	if err := spawnDaemon(ep); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := ipc.Dial(ep); err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not come up within 5s")
}

// runDaemon is the daemon's main function (the `__server` subcommand).
func runDaemon(ep ipc.Endpoint) error {
	// Every pane shell inherits this, so `ngmux` run from inside a pane can
	// tell it is nested and refuse (see refuseIfNested).
	_ = os.Setenv(nestGuardEnv, ep.Name)

	// The daemon's stdio goes to os.DevNull (see spawnDaemon) so a crash used
	// to leave no trace at all unless NGMUX_DEBUG happened to be set for that
	// run. Always log to a file instead: NGMUX_DEBUG additionally mirrors to
	// stderr, for someone running `__server` by hand in a terminal.
	logger := log.New(io.Discard, "", 0)
	debugOn := os.Getenv("NGMUX_DEBUG") != ""
	if logFile, err := openDaemonLog(ep.Name); err == nil {
		w := io.Writer(logFile)
		if debugOn {
			w = io.MultiWriter(logFile, os.Stderr)
		}
		logger = log.New(w, "ngmuxd ", log.LstdFlags)
		// Route unrecovered fatal panics (the ones nothing in the codebase
		// could plausibly recover from, e.g. a runtime-detected data race or
		// an out-of-memory) to the same file, so their stack trace is not
		// lost the moment stdio goes to /dev/null.
		_ = debug.SetCrashOutput(logFile, debug.CrashOptions{})
	} else if debugOn {
		// No log file (e.g. UserCacheDir unavailable): fall back to the
		// previous behavior rather than running completely silent.
		logger = log.New(os.Stderr, "ngmuxd ", log.LstdFlags)
	}
	return server.Run(ep, 80, 24, logger)
}

// maxDaemonLogSize is the size threshold, checked once at startup, past which
// the daemon's log file is truncated instead of appended to forever.
const maxDaemonLogSize = 5 * 1024 * 1024 // 5 MiB

// daemonLogPath returns the path to the daemon's persistent log file for the
// named endpoint: <cacheDir>/ngmux/ngmuxd-<sanitized name>.log. cacheDir is a
// parameter (rather than calling os.UserCacheDir() here) so tests can point
// it at a t.TempDir() instead of the real user cache directory.
func daemonLogPath(cacheDir, epName string) string {
	return filepath.Join(cacheDir, "ngmux", "ngmuxd-"+sanitizeLogName(epName)+".log")
}

// sanitizeLogName keeps an endpoint name safe to use as a file name
// component: only letters, digits, '-' and '_' survive; everything else
// (path separators in particular, since ep.Name is not validated as a bare
// filename anywhere upstream) becomes '_'.
func sanitizeLogName(name string) string {
	if name == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// openDaemonLogFile opens (creating if needed) the log file at path for
// appending, truncating it first if it has already grown past
// maxDaemonLogSize. It creates the parent directory (0o700) and the file
// itself (0o600) if they don't exist yet.
func openDaemonLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxDaemonLogSize {
		if err := os.Truncate(path, 0); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// openDaemonLog opens the daemon's log file for ep.Name under the OS user
// cache directory. Any failure (cache dir unavailable, permission denied, ...)
// is returned so the caller can fall back to the previous silent-unless-debug
// behavior rather than failing the whole daemon over logging.
func openDaemonLog(epName string) (*os.File, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return openDaemonLogFile(daemonLogPath(cacheDir, epName))
}
