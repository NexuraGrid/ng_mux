# default-shell-args

## Objective
Let `set default-shell` carry arguments, e.g. `set default-shell pwsh -NoLogo`.

## Problem
`config.set` stores the whole value (`pwsh -NoLogo`) as `DefaultShell`, and
`startPane` passes it as `ptyx.Config.Prog`, so the pty tries to execute a
program literally named `pwsh -NoLogo` and the pane fails to start.

## Scope
- Parse `default-shell` as a quote-aware argv: first word is the program, the
  rest are args. Double or single quotes group words
  (`"C:\Program Files\PowerShell\7\pwsh.exe" -NoLogo`).
- Backward compatibility: when the whole unsplit value names an existing file
  (an unquoted path with spaces, which works today), keep it as the program
  with no args.
- Thread prog + args from config through server/session/window/pane to
  `ptyx.Config{Prog, Args}`.
- Window default name keeps using the program's base name.
- Unchanged: an unset `default-shell` still uses the platform default and its
  default args (`-l` on Unix, `-NoLogo` for PowerShell on Windows).

## Acceptance criteria
- `set default-shell pwsh -NoLogo` starts `pwsh` with `-NoLogo`.
- `set default-shell /bin/bash` behaves exactly as before.
- Quoted paths with spaces work; unclosed quotes produce a config warning.
- README documents the argument form.

## Checks
- `go test ./...`, `go vet ./...`

## Tasks
- [x] T1 — argv parsing in config + threading to ptyx + tests + README (route: delegated writer; trigger: 2+ non-trivial files)

### T1 evidence (pending commit)
- Design: `config.Config` gains `DefaultShellArgs []string` (`parseShell`:
  quote-aware split, existing-file fallback, unclosed quote → warning). In
  `internal/server`, `sessionOpts` mirrors it with `defaultShellArgs`;
  `session.shellArgv()` builds the argv (nil = platform default), and
  window/`paneFactory`/`startPane` carry `shell []string`, which `startPane`
  maps to `ptyx.Config{Prog, Args}`. ptyx unchanged.
- RED: new config tests failed before `parseShell` (e.g. `prog = "pwsh -NoLogo"`
  args `[]`; no unclosed-quote warning).
- GREEN: `go build ./...`, `go vet ./...`, `go test ./...`,
  `GOOS=windows go build ./...`, `GOOS=darwin go build ./...` all pass.
- Server test `TestDefaultShellArgsReachPaneFactory` covers first pane + split.

## Progress
- Branch: `feat/default-shell-args`
- Delivery strategy: ask-on-risk (forecast ~150 lines, under budget)

- Commit: `8971f2a` feat(config): accept arguments in default-shell
- Review assess: medium, review_due=false (under_budget)
