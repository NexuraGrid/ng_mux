# p2-hardening

Objective: close the P2 security and diagnostics items from `odd/tasks/audit-backlog.md` (B3, B4, B5); ngmux runs on shared servers.
Branch: `feat/p2-hardening`, one PR to `main` (forecast ~250 authored lines, under the 400 budget). Test runner: `go test -race ./...`; Windows code checked with `GOOS=windows go vet ./...` and validated by the user on real Windows.

## Specs

- S1 (B4) "Unix socket dir falls back to `/tmp/ngmux-<uid>` when `XDG_RUNTIME_DIR` is unset (Docker, no systemd) and `MkdirAll` does not check who owns an existing dir. Fix: lstat, require owner == uid and mode 0700, refuse otherwise (as tmux does). `internal/ipc/ipc_unix.go`." Applies to both Listen and Dial; a symlink in place of the dir is refused.
- S2 (B5) "Daemon startup errors go to `/dev/null`, not the daemon log (seen with a socket path over 108 bytes: `bind: invalid argument`). Fix: open the log before listening and log the error; have the client print the log path when the daemon does not come up. `cmd/ngmux/main.go`, `cmd/ngmux/spawn_*.go`."
- S3 (B3) "Windows named pipe uses the default DACL (Everyone can open it for reading) and a predictable name `\\.\pipe\ngmux-<USERNAME>-<name>`, so another local user can create it first (pipe squatting) and receive the victim's keystrokes. Fix: explicit SDDL limited to the user's SID, and verify the server process owner on Dial. `internal/ipc/ipc_windows.go`."

## Tasks

| ID | Specs | Route | Status | Commit |
|----|-------|-------|--------|--------|
| T1 | S1 | inline | pending | |
| T2 | S2 | inline | pending | |
| T3 | S3 | inline (no Windows runner here: vet + user validation) | pending | |

Route evidence: no Writer trigger (small sequential units, no context backstop), so the parent writes inline. S1 and S3 are high risk (item 2, security): independent verifier at feature end.

## Log

- L1 (2026-10-22, user): "Todavia no he probado B1 despues lo provamos asi que por ahora por favor continuemos con P2, si tienes razon con lo de control V"
- L2 Log is already opened before Listen (`runDaemon`); the gap is that `server.Run`'s error is returned to a stderr wired to /dev/null and never logged.
