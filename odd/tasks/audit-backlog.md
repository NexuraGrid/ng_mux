# audit-backlog

Pending work from the 2026-10-21 stability/fidelity audit, after P0 (freeze, bracketed paste) and P1 (terminal-fidelity) shipped.
Pick items from here into their own feature document when work resumes. Runner: `go test -race ./...`.

## Status (2026-10-22)

Done and merged to `main`
- v0.9.0: P0 (freeze, bracketed paste), P1 (terminal fidelity).
- v0.9.1: B3-B5 security/diagnostics (PR #36), B15 fzf/zoxide colors (PR #37).
- v0.9.2: B22-B23 phantom session `0` and detach on session close (PR #38); B17, B18, B19, B21 dial errors, CLI quoting, socket dir race, truecolor/colon SGR (PR #39).
- B2 image paste: checked by the user.
- Decision: no key binding to kill a whole session (L6).

Pending
- Needs the user on real Windows: B16 elevated vs non-elevated terminal; B1 heavy-log workers (new/attach/Ctrl+V/Ctrl+C already work).
- Advisories: B6-B12, B20.
- Performance: B13 (vt10x `clear()` on log floods).
- Product: B14 (mouse capture default).
- Follow-ups from PR #39 (`odd/tasks/real-bugs-b17-b21.md`): harden `TestExecArgsKeepQuoting` (it also matches the echoed input); fd-based socket dir check for a misconfigured non-sticky base dir.

## Specs

Validation (needs a human with the real environment)

- B1 Partly checked (2026-10-22, user on real Windows): `new`, `attach`, Ctrl+V and Ctrl+C work; the heavy-log workers run is still pending. Test the P0/P1 build on real Windows (ConPTY) with the workers whose log output froze ngmux; confirm Ctrl-b d and Ctrl-C keep working under heavy logs.
- B2 Done (2026-10-22): image paste works via Alt+V, drag-and-drop and the clipboard history; Ctrl+V and Ctrl+Shift+V do not (Windows Terminal takes them for text paste; Claude Code on Windows binds image paste to Alt+V). User: "funciona de 3 formas el pegaod de la imagen".

P2 security (ngmux runs on shared servers)

- B3 Done in v0.9.1 (PR #36); still needs the real-Windows check in B16. Windows named pipe uses the default DACL (Everyone can open it for reading) and a predictable name `\\.\pipe\ngmux-<USERNAME>-<name>`, so another local user can create it first (pipe squatting) and receive the victim's keystrokes. Fix: explicit SDDL limited to the user's SID, and verify the server process owner on Dial. `internal/ipc/ipc_windows.go`.
- B4 Done in v0.9.1 (PR #36). Unix socket dir falls back to `/tmp/ngmux-<uid>` when `XDG_RUNTIME_DIR` is unset (Docker, no systemd) and `MkdirAll` does not check who owns an existing dir. Fix: lstat, require owner == uid and mode 0700, refuse otherwise (as tmux does). `internal/ipc/ipc_unix.go`.

P2 diagnostics

- B5 Done in v0.9.1 (PR #36). Daemon startup errors go to `/dev/null`, not the daemon log (seen with a socket path over 108 bytes: `bind: invalid argument`). Fix: open the log before listening and log the error; have the client print the log path when the daemon does not come up. `cmd/ngmux/main.go`, `cmd/ngmux/spawn_*.go`.

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

- B15 Done in v0.9.1 (PR #37, 2026-10-22): root cause was the CSI parser stopping at an empty parameter, and fzf starts nearly every SGR with one (`ESC[;1;38;5;161;48;5;236m`, also when re-sending the preview's `ls` colors), so SGR became a plain reset. Verified with real fzf 0.74.5 and GNU ls output through the emulator. Not caused by env (pane has TERM=xterm-256color, COLORTERM=truecolor). The truecolor-below-256 collision and colon SGR below are still open. Original report: `zi` (zoxide interactive, fzf) loses its colors inside ngmux; in a plain terminal the same command shows them. User report (2026-10-21): "cuadno busco algo por zi dentro de ngmux no aparecen los colores [...] quita los colores y la persona no peude saber bien a cual proyecto va a entrar porque aparece un blanco super raro". Observed in ngmux vs plain terminal (screenshots, `zi ng`): the selected row has no highlight background, the red pointer/gutter bar becomes a white bar on every row, and the preview (`ls --color` of the directory) shows directories uncolored. The shell prompt keeps its colors, so plain 16-color SGR works. Acceptance: `zi` inside ngmux looks like in the plain terminal (selected-row highlight, colored gutter/pointer, colored preview).
  - First step: reproduce and compare `env | rg 'TERM|COLOR'` inside and outside ngmux, and capture fzf's raw output (`script`) in both to see whether fzf emits no colors (environment/detection: `NO_COLOR`, `TERM`, `COLORTERM`; fzf in no-color mode looks exactly like the screenshot) or ngmux drops/misrenders them.
  - Pane env: `internal/ptyx/ptyx.go:83` appends `TERM=xterm-256color` after `os.Environ()`, so the daemon's own `TERM` (and any `NO_COLOR`) also reaches the pane, and `COLORTERM` is never set.
  - Emulator: `internal/vt10x/state.go:746-783` stores truecolor as `Color(r<<16|g<<8|b)` in the same space as palette indexes, so any RGB value below 256 (e.g. `#0000c8`) is rendered as palette color N (`internal/render/render.go:709`, `internal/vterm/vterm.go:665` test `< 256`). Colon-separated SGR (`38:2::r:g:b`) is not parsed.

Found while doing P2 and B15 (2026-10-22)

- B16 Partly checked (2026-10-22): non-elevated `new` and `attach` work; the elevated vs non-elevated run is still pending (user will do it later). Validate v0.9.1 on real Windows: `ngmux new`, `attach` and `ls` from a normal and from an elevated terminal. A non-elevated client may refuse a daemon started elevated ("cannot verify the owner of server process N"), because OpenProcessToken can be denied across integrity levels; if it happens, rework the owner check (e.g. compare the pipe's owner SID instead of opening the process token). `internal/ipc/ipc_windows.go` checkServerOwner.
- B17 Fixed in PR #39 (`odd/tasks/real-bugs-b17-b21.md`). `ngmux ls`, `attach`, `kill-session` and `kill-server` turn every Dial error into "no server running", so an unsafe socket dir or a foreign pipe owner only shows up through `ngmux new`. `cmd/ngmux/main.go`.
- B18 Fixed in PR #39 (`odd/tasks/real-bugs-b17-b21.md`). `ngmux send-keys "echo a b" Enter` from the shell loses the quoting (client.Exec joins os.Args with spaces), so the pane gets a broken command; `ngmux run 'send-keys "echo a b" Enter'` works. `cmd/ngmux/main.go` default case.
- B19 Fixed in PR #39 (`odd/tasks/real-bugs-b17-b21.md`). Two `ngmux` started at once under a restrictive umask can race `Mkdir`/`Chmod` of a fresh socket dir and one refuses it as unsafe. `internal/ipc/ipc_unix.go`.
- B20 `TestRunDaemonLogsListenError` assumes `XDG_CACHE_HOME`, which macOS ignores (CI runs Linux and Windows only). `cmd/ngmux/daemon_log_test.go`.
- B21 Fixed in PR #39 (`odd/tasks/real-bugs-b17-b21.md`). From B15: a truecolor value below 256 (e.g. `#0000c8`) is rendered as palette color N, and colon SGR sub-parameters (`38:2::r:g:b`) stop parsing. `internal/vt10x/state.go`, `internal/vt10x/csi.go`.

Session lifecycle (user report, 2026-10-22)

- B22 Fixed in PR #38. On a fresh server, `ngmux new -t test` left two sessions, `0` and `test`; the user never created `0`. Only the first session of a new server; later `ngmux new -t test2` created just `test2`. Root cause: the daemon always created session `0` at startup (`internal/server/server.go` Run) before the client's attach created the one it named. Fix: no session up front; the first attach creates the one it names. Test: `TestFirstNamedSessionIsTheOnlyOne` (e2e).
- B23 Fixed in PR #38. Closing a session (last window, `kill-session`) moved its attached clients into another live session instead of detaching them, so a careless user who keeps typing could kill a different session. User: "si soy una persona descuidada la peudo borrar eso es peligroso [...] despues de todo son sessiones distintas". Root cause: `sessionEmptied` in `internal/server/server.go` reassigned clients to a surviving session. Fix: detach them (bye), like tmux's default detach-on-destroy; the server still stops when no session is left. Tests: `TestSessionEmptiedDetachesItsClients`, `TestSessionEmptiedNeverMovesAClient`, `TestKillSessionDetachesItsClient` (e2e).

Product

- B14 Mouse capture is on by default, which hides the terminal's own selection (Shift-drag works); consider whether the default should change or be documented more visibly.

## Tasks

| ID | Specs | Route | Status |
|----|-------|-------|--------|
| - | B2 | - | done (user check) |
| - | B3-B5 | `odd/tasks/p2-hardening.md` | done, v0.9.1 (PR #36) |
| - | B15 | inline | done, v0.9.1 (PR #37) |
| - | B22-B23 | inline, `fix/session-lifecycle` | done, v0.9.2 (PR #38) |
| - | B17-B19, B21 | inline, `odd/tasks/real-bugs-b17-b21.md` | done, v0.9.2 (PR #39) |
| - | B1, B6-B14, B16, B20 | - | open, not planned yet |

## Log

- L1 (2026-10-21, user): "anota por favor lo que falta osea el ps2 y los demas para continuar despues por favor"
- L2 P0 and P1 shipped through the stacked PRs to main and the v0.9.0 release.
- L3 (2026-10-21, user): "anota eso de forma clara apra hacerlo y corregirlo por favor" (B15, zoxide/fzf colors).
- L4 (2026-10-22, user): "Si por favor y guarda ya lo hecho y lo que falta y genera la release con los cmabios hechos hasta ahora". P2 (PR #36) and B15 (PR #37) merged to main and released as v0.9.1; B16-B21 added from the verifier and review advisories. Next: B16 (real Windows check), then B1.
- L5 (2026-10-22, user): "Si por favor arregla los bugs y anota para que sepamos y nos quede como historial de que se detecto y que se van a corregir y en cuento a lo de la terminal elevada todavia no he hecho la prueba edspues la hago". Added B22-B23 (phantom session `0`, switch-on-close) and fixed them on `fix/session-lifecycle`; recorded the partial Windows checks in B1 and B16.
- L6 (2026-10-22, user): "no tranquilo no se aplicara un comando para cerrar una session entera esa mejor asi como esta". Decision: no key binding to kill a whole session; it stays `Ctrl-b :` `kill-session` or `ngmux kill-session -t NAME`, so it cannot be triggered by accident.
- L7 (2026-10-22, user): "No todavia no ahora arreglemos esto" (no v0.9.2 release yet; fix B18, B17, B21, B19). Done on `fix/real-bugs-b17-b21`, see `odd/tasks/real-bugs-b17-b21.md`.
- L8 (2026-10-22, user): "Ok por davor guarda todo lo hecho y anota lo que ya quedo liso y lo que nos falta por hacer por favor". Added the Status section; PR #38 and #39 merged, v0.9.2 not released yet.
- L9 (2026-10-22, user): "si por favor" (release). v0.9.2 tagged on `1bb191a` and published by the release workflow with PR #38 and #39; 5 binaries + SHA256SUMS.
