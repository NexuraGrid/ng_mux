# real-bugs-b17-b21

Fix the four real bugs B17, B18, B19 and B21 from `odd/tasks/audit-backlog.md`, one work-unit commit each.
Branch `fix/real-bugs-b17-b21`; delivery `ask-on-risk`; runner `go test -race ./...`.

## Specs

- S1 (B18) "`ngmux send-keys "echo a b" Enter` pierde las comillas porque se unen los args con espacios." The `ngmux <command> args...` form must deliver each shell argument as one token, so the pane gets `echo a b`. `ngmux run '<line>'` keeps taking a raw command line.
- S2 (B17) "`ls`, `attach` y `kill-*` muestran cualquier error de Dial como "no server running" y esconden la causa real." Only `ipc.ErrNoServer` prints "no server running"; any other Dial error (e.g. an unsafe socket dir) is shown as is.
- S3 (B19) "una carrera entre `Mkdir` y `Chmod` del socket dir cuando arrancan dos ngmux a la vez con umask restrictivo." A fresh socket dir that is ours and has no group/other bits must be accepted (and repaired to 0700), not refused as unsafe; a dir owned by someone else or with group/other bits is still refused.
- S4 (B21) "un truecolor menor a 256 se pinta como color de paleta, y el SGR con dos puntos (`38:2::r:g:b`) no se parsea." `ESC[38;2;0;0;200m` must render as RGB (0,0,200), not palette 200; `38:2::r:g:b`, `38:2:r:g:b` and `38:5:n` (and the `48` forms) must set the color like their semicolon forms and not drop later parameters.

## Tasks

| ID | Specs | Route | Status |
|----|-------|-------|--------|
| T1 | S1 | inline | done, `05a4457`; RED: pane got `echoQAQB: not found`; GREEN e2e + JoinArgs unit + tokenizer round trip |
| T2 | S2 | inline | done, `69e86aa`; RED: 4 commands said "no server running" on an unsafe dir; GREEN + no-server message kept |
| T3 | S3 | inline | done, `3224a46` + `8286008` (verifier advisories); RED: own 0500 dir refused; independent verifier PASS, no blockers |
| T4 | S4 | inline | done, `0d3bec8`; RED: `;38;5;200` for #0000c8 and colon forms ignored; GREEN; FuzzTermOps/FuzzWrite 15 s each, no crash |

Route evidence: no Writer trigger (units are small and sequential in one tree), so the parent writes inline.

## Log

- L1 (2026-10-22, user): "No todavia no ahora arreglemos esto : Bugs reales (se pueden hacer ya) - B18: ngmux send-keys "echo a b" Enter pierde las comillas porque se unen los args con espacios. - B17: ls, attach y kill-* muestran cualquier error de Dial como "no server running" y esconden la causa real. - B21: un truecolor menor a 256 se pinta como color de paleta, y el SGR con dos puntos (38:2::r:g:b) no se parsea. - B19: una carrera entre Mkdir y Chmod del socket dir cuando arrancan dos ngmux a la vez con umask restrictivo."
- L2 Map: B18 `cmd/ngmux/main.go` default case joins `os.Args` (server `tokenize` in `internal/server/command.go` honours quotes and concatenates adjacent quoted segments). B17 `cmd/ngmux/main.go` cmdAttach/cmdList/cmdKillSession/cmdKillServer. B19 `internal/ipc/ipc_unix.go` socketDir. B21 vt10x packs RGB as `r<<16|g<<8|b` in the palette space (`state.go` SGR 38/48, `str.go` OSC overrides), vterm `convColor` keeps it, `render` treats `< 256` as palette; CSI `parse` in `csi.go` stops at a param with `:`.
- L3 Done. `go test -race ./...` green; `GOOS=windows go vet` clean for cmd/ngmux and internal/ipc. T3 (security item 2) got an independent verifier: PASS; its advisories applied in `8286008` (error message names the real rule, more modes tested). Advisory left open: an fd-based check (O_NOFOLLOW + fstat/fchmod) would also close the Lstat/Chmod window under a non-sticky, world-writable base dir, which already existed before. RDD assess on `main..HEAD`: risk medium, review_due false (under_budget). Branch is about 430 changed lines, mostly tests.
- L4 Native review review-80ee4793b623f8bb (lens reliability, user granted): approved and acknowledged. Advisories: (a) JoinArgs and other whitespace: moot, `tokenize` splits only on space and tab; (b) convColor drops untagged values >= 256: no producer left (only state.go SGR 38/48 and str.go OSC built raw RGB, all now use RGB()); (c) `TestExecArgsKeepQuoting` also matches the echoed input, so it proves one token but not that the command ran: open, later test hardening.
