//go:build !windows

package e2e

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestStressWorkersFloodWhileScrolling is the acceptance test for the v0.6.0
// stability work. It reproduces what teammates run inside ngmux, all at once
// in one session: a worker flooding logs, a process emitting random bytes
// (every kind of escape sequence), a process flooding terminal queries while
// never reading its stdin, and an idle pane. Meanwhile the attached client
// scrolls with the wheel, enters and leaves copy-mode, and resizes its
// terminal. Afterwards the daemon and client must still be up, every pane
// must still respond to input, and nothing may have panicked.
func TestStressWorkersFloodWhileScrolling(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test; skipped in -short")
	}
	// Panes inherit the daemon's working directory. Query answers reach the
	// shells as input, and one of them ("\x1b[>84;0;0c") reads as a redirect
	// to a file named 84, so keep the shells out of the source tree.
	t.Chdir(t.TempDir())
	h := newHarness(t)
	time.Sleep(300 * time.Millisecond)

	for _, line := range []string{"split-window -h", "split-window -v", "split-window -v", "select-layout tiled"} {
		if out := execCmd(t, h.ep, line); strings.HasPrefix(out, "error") {
			t.Logf("%s: %s", line, out)
		}
	}
	time.Sleep(300 * time.Millisecond)

	const chaos = 6 * time.Second
	workloads := []string{
		// pane 0: a worker flooding structured log lines
		`timeout 7 yes 'worker-0 2026-09-13T10:00:00Z INFO job=42 processed ok in 12ms'`,
		// pane 1: random bytes, i.e. every escape sequence, then a full reset
		`timeout 7 cat /dev/urandom; printf '\033c'`,
		// pane 2: terminal queries from a process that never reads its stdin
		`timeout 5 sh -c 'while :; do printf "\033[6n\033[c\033[>c"; done'`,
		// pane 3 stays idle
	}
	for i, w := range workloads {
		execCmd(t, h.ep, `send-keys -t 0.`+string(rune('0'+i))+` "`+strings.ReplaceAll(w, `"`, `\"`)+`" Enter`)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		sizes := [][2]uint16{{80, 24}, {120, 40}, {100, 30}}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			switch {
			case i%40 == 39:
				sz := sizes[(i/40)%len(sizes)]
				_ = pty.Setsize(h.master, &pty.Winsize{Cols: sz[0], Rows: sz[1]})
				_ = syscall.Kill(os.Getpid(), syscall.SIGWINCH)
			case i%25 == 24:
				h.send("\x02[") // copy-mode on the active pane
				time.Sleep(5 * time.Millisecond)
				h.send("q")
			case i%3 == 2:
				h.send("\x1b[<65;10;4M") // wheel down over pane 0
			default:
				h.send("\x1b[<64;10;4M") // wheel up over pane 0
			}
			if i%200 == 199 {
				h.reset() // keep the captured terminal output bounded
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	time.Sleep(chaos)
	close(stop)
	wg.Wait()

	select {
	case err := <-h.srvErr:
		t.Fatalf("daemon exited during the stress run: %v", err)
	case err := <-h.cliErr:
		t.Fatalf("client detached during the stress run: %v", err)
	default:
	}

	_ = pty.Setsize(h.master, &pty.Winsize{Cols: 120, Rows: 40})
	_ = syscall.Kill(os.Getpid(), syscall.SIGWINCH)
	time.Sleep(8 * time.Second) // let the timed workloads finish

	// The wheel may have left pane 0 in copy-mode, whose view is anchored and
	// would hide the probe's output. One more wheel-up guarantees copy-mode on
	// pane 0 (and focuses it); q then leaves it without typing into the shell.
	h.send("\x1b[<64;10;4M")
	time.Sleep(100 * time.Millisecond)
	h.send("q")
	time.Sleep(100 * time.Millisecond)

	for pane := 0; pane < 4; pane++ {
		marker := "ALIVE_" + string(rune('A'+pane))
		target := `send-keys -t 0.` + string(rune('0'+pane))
		// Answers to the queries the random and query workloads emitted arrive
		// as unterminated input to the shell, exactly as in a real terminal.
		// Enter runs (and discards) that line so the probe starts on a clean one.
		execCmd(t, h.ep, target+` "" Enter`)
		time.Sleep(300 * time.Millisecond)
		h.reset()
		start := time.Now()
		// Split the marker in the typed command so only the command's output,
		// never its echo, can match.
		execCmd(t, h.ep, `send-keys -t 0.`+string(rune('0'+pane))+` "printf '`+marker[:4]+`''`+marker[4:]+`\n'" Enter`)
		if !h.waitFor(marker, 10*time.Second) {
			t.Fatalf("pane %d did not respond after the stress run; screen tail:\n%q", pane, tail(h.screen(), 600))
		}
		t.Logf("pane %d answered in %v", pane, time.Since(start).Round(time.Millisecond))
	}

	if logs := h.logText(); strings.Contains(logs, "panic") {
		t.Fatalf("daemon logged a panic during the stress run:\n%s", logs)
	}

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("heap in use after the stress run: %d MiB", ms.HeapInuse>>20)
	if ms.HeapInuse > 512<<20 {
		t.Fatalf("heap in use %d MiB after the stress run, want < 512 MiB", ms.HeapInuse>>20)
	}
}
