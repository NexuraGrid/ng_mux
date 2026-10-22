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
| T1 | S1 | inline | pending |
| T2 | S2 | inline | pending |
| T3 | S3 | inline | pending |
| T4 | S4 | inline | pending |

Route evidence: no Writer trigger (units are small and sequential in one tree), so the parent writes inline.

## Log

- L1 (2026-10-22, user): "No todavia no ahora arreglemos esto : Bugs reales (se pueden hacer ya) - B18: ngmux send-keys "echo a b" Enter pierde las comillas porque se unen los args con espacios. - B17: ls, attach y kill-* muestran cualquier error de Dial como "no server running" y esconden la causa real. - B21: un truecolor menor a 256 se pinta como color de paleta, y el SGR con dos puntos (38:2::r:g:b) no se parsea. - B19: una carrera entre Mkdir y Chmod del socket dir cuando arrancan dos ngmux a la vez con umask restrictivo."
- L2 Map: B18 `cmd/ngmux/main.go` default case joins `os.Args` (server `tokenize` in `internal/server/command.go` honours quotes and concatenates adjacent quoted segments). B17 `cmd/ngmux/main.go` cmdAttach/cmdList/cmdKillSession/cmdKillServer. B19 `internal/ipc/ipc_unix.go` socketDir. B21 vt10x packs RGB as `r<<16|g<<8|b` in the palette space (`state.go` SGR 38/48, `str.go` OSC overrides), vterm `convColor` keeps it, `render` treats `< 256` as palette; CSI `parse` in `csi.go` stops at a param with `:`.
