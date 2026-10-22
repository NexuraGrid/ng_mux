# audit-backlog

Pending work from the 2026-10-21 stability/fidelity audit, after P0 (freeze, bracketed paste) and P1 (terminal-fidelity) shipped.
Pick items from here into their own feature document when work resumes. Runner: `go test -race ./...`.

## Specs

Validation (needs a human with the real environment)

- B1 Test the P0/P1 build on real Windows (ConPTY) with the workers whose log output froze ngmux; confirm Ctrl-b d and Ctrl-C keep working under heavy logs.
- B2 Done (2026-10-22): image paste works via Alt+V, drag-and-drop and the clipboard history; Ctrl+V and Ctrl+Shift+V do not (Windows Terminal takes them for text paste; Claude Code on Windows binds image paste to Alt+V). User: "funciona de 3 formas el pegaod de la imagen".

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

Terminal fidelity

- B15 `zi` (zoxide interactive, fzf) loses its colors inside ngmux; in a plain terminal the same command shows them. User report (2026-10-21): "cuadno busco algo por zi dentro de ngmux no aparecen los colores [...] quita los colores y la persona no peude saber bien a cual proyecto va a entrar porque aparece un blanco super raro". Observed in ngmux vs plain terminal (screenshots, `zi ng`): the selected row has no highlight background, the red pointer/gutter bar becomes a white bar on every row, and the preview (`ls --color` of the directory) shows directories uncolored. The shell prompt keeps its colors, so plain 16-color SGR works. Acceptance: `zi` inside ngmux looks like in the plain terminal (selected-row highlight, colored gutter/pointer, colored preview).
  - First step: reproduce and compare `env | rg 'TERM|COLOR'` inside and outside ngmux, and capture fzf's raw output (`script`) in both to see whether fzf emits no colors (environment/detection: `NO_COLOR`, `TERM`, `COLORTERM`; fzf in no-color mode looks exactly like the screenshot) or ngmux drops/misrenders them.
  - Pane env: `internal/ptyx/ptyx.go:83` appends `TERM=xterm-256color` after `os.Environ()`, so the daemon's own `TERM` (and any `NO_COLOR`) also reaches the pane, and `COLORTERM` is never set.
  - Emulator: `internal/vt10x/state.go:746-783` stores truecolor as `Color(r<<16|g<<8|b)` in the same space as palette indexes, so any RGB value below 256 (e.g. `#0000c8`) is rendered as palette color N (`internal/render/render.go:709`, `internal/vterm/vterm.go:665` test `< 256`). Colon-separated SGR (`38:2::r:g:b`) is not parsed.

Product

- B14 Mouse capture is on by default, which hides the terminal's own selection (Shift-drag works); consider whether the default should change or be documented more visibly.

## Tasks

| ID | Specs | Route | Status |
|----|-------|-------|--------|
| - | B3-B5 | - | moved to `odd/tasks/p2-hardening.md` |
| - | B1, B6-B15 | - | open, not planned yet |

## Log

- L1 (2026-10-21, user): "anota por favor lo que falta osea el ps2 y los demas para continuar despues por favor"
- L2 P0 and P1 shipped through the stacked PRs to main and the v0.9.0 release.
- L3 (2026-10-21, user): "anota eso de forma clara apra hacerlo y corregirlo por favor" (B15, zoxide/fzf colors).
