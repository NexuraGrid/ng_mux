# audit-backlog

Pending work from the 2026-10-21 stability/fidelity audit, after P0 (freeze, bracketed paste) and P1 (terminal-fidelity) shipped.
Pick items from here into their own feature document when work resumes. Runner: `go test -race ./...`.

## Specs

Validation (needs a human with the real environment)

- B1 Test the P0/P1 build on real Windows (ConPTY) with the workers whose log output froze ngmux; confirm Ctrl-b d and Ctrl-C keep working under heavy logs.
- B2 Test image paste into Claude Code inside ngmux on Windows (Ctrl+V / Alt+V, and drag-and-drop of an image file).

P2 security (ngmux runs on shared servers)

- B3 Windows named pipe uses the default DACL (Everyone can open it for reading) and a predictable name `\\.\pipe\ngmux-<USERNAME>-<name>`, so another local user can create it first (pipe squatting) and receive the victim's keystrokes. Fix: explicit SDDL limited to the user's SID, and verify the server process owner on Dial. `internal/ipc/ipc_windows.go`.
- B4 Unix socket dir falls back to `/tmp/ngmux-<uid>` when `XDG_RUNTIME_DIR` is unset (Docker, no systemd) and `MkdirAll` does not check who owns an existing dir. Fix: lstat, require owner == uid and mode 0700, refuse otherwise (as tmux does). `internal/ipc/ipc_unix.go`.

P2 diagnostics

- B5 Daemon startup errors go to `/dev/null`, not the daemon log (seen with a socket path over 108 bytes: `bind: invalid argument`). Fix: open the log before listening and log the error; have the client print the log path when the daemon does not come up. `cmd/ngmux/main.go`, `cmd/ngmux/spawn_*.go`.

Review advisories left open (native RDD reviews review-a405a61d835cd501 and review-6f9c64037469dbad, end-of-feature verifier)

- B6 Pasting while the pane is in copy-mode: the paste start marker's ESC reaches copy-mode as a key (e.g. exits it). `internal/server/session.go` routeInput skips filterPaste.
- B7 `send-keys …` immediately followed by `kill-pane` can drop the queued bytes (the input queue is discarded on close). `internal/server/input.go`.
- B8 A single paste over 16 MiB (`maxPendingInput`) is dropped whole even into a healthy pane.
- B9 `pane.write` logs one line per refused push after the pane closed (log noise). `internal/server/pane.go`.
- B10 Timing-based tests (pasteIdleTimeout 1s, escBodyWait 50ms, syncTimeout 200ms) may flake on a loaded CI runner; inject the clock/timeouts.
- B11 Erasing (EL/ED) while dim, hidden or strikethrough is active gives the blanks that attribute (same class as the existing underline/reverse behavior; xterm only carries the background color).
- B12 In the `:` prompt, a key typed within 50 ms after Esc is taken as part of an escape sequence (tradeoff for split arrows on slow links).

Performance

- B13 Log floods: the next hot spot is vt10x `clear()` of the scrolled-in row (~17% CPU). Reducing it needs emulator rework. Current: 26 MB/s, ~450k lines/s in `BenchmarkWriteLogFlood`; `seq 1 3000000` ~5 s vs 1-2 s in a plain terminal.

Product

- B14 Mouse capture is on by default, which hides the terminal's own selection (Shift-drag works); consider whether the default should change or be documented more visibly.

## Tasks

| ID | Specs | Route | Status |
|----|-------|-------|--------|
| - | B1-B14 | - | open, not planned yet |

## Log

- L1 (2026-10-21, user): "anota por favor lo que falta osea el ps2 y los demas para continuar despues por favor"
- L2 P0 and P1 shipped through the stacked PRs to main and the v0.9.0 release.
