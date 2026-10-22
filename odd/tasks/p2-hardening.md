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
| T1 | S1 | inline | done | 81cb8ec (RED: 0755 dir, symlink, 0777 dir accepted; GREEN full suite -race) |
| T2 | S2 | inline | done | 30e1a64 (RED: daemon log empty after bind error; client message test written with the fix, no separate RED; GREEN full suite -race) |
| T3 | S3 | inline (no Windows runner here: vet + user validation) | code done, Windows tests pending (CI windows-latest + user) | 639f25f (no RED possible on Linux; GOOS=windows vet + test compile OK) |

Route evidence: no Writer trigger (small sequential units, no context backstop), so the parent writes inline. S1 and S3 are high risk (item 2, security): independent verifier at feature end.

## Log

- L1 (2026-10-22, user): "Todavia no he probado B1 despues lo provamos asi que por ahora por favor continuemos con P2, si tienes razon con lo de control V"
- L2 Log is already opened before Listen (`runDaemon`); the gap is that `server.Run`'s error is returned to a stderr wired to /dev/null and never logged.
- L3 Independent verifier (2026-10-22): S1 pass, S2 pass, S3 pass by code review (Windows tests not runnable on Linux); no blockers. Probes: 0755 dir, symlink and regular file refused by `ngmux new` with no daemon spawned; fresh dir created 0700 and the normal new/ls/kill-server path works; 146-byte socket path logs `bind: invalid argument` and the client names the log. Advisories: `ls`/`attach`/`kill-*` still mask any Dial error as "no server running" (pre-existing); a non-elevated client may refuse an elevated same-user daemon (fail closed, check on Windows); foreign-uid dir branch covered by reading only (needs root).
- L4 Native review (2026-10-22): assess raised to high (item 2), user granted, lenses risk/resilience/reliability, approved and acknowledged (review-457b1396ff6f2883, authority burned). Advisories: R4 `socketDir` never chmods the dir it just created, so a umask or default POSIX ACL can leave a fresh dir non-0700 and then refused; R1/R3 a non-elevated client may be refused by an elevated same-user daemon (OpenProcessToken); R3 Windows tests not run yet.
- Next: user validates on real Windows (normal and elevated terminals); CI windows-latest runs `internal/ipc` Windows tests on the PR.
