# multiclient-nested-copy

## Objective
Fix user-reported issues: a second client corrupts the first, nested tmux can't get Ctrl-b, mouse copy needs Shift and crosses pane borders, and config/keybinding conflicts.

## Problem / Why
- Session size is last-writer-wins (`server.go` attach/resize call `sess.resize` with the newest client's size); all clients receive one frame, so a differently sized terminal corrupts the other. Refresh never re-applies the client's size.
- Prefix is always captured; only `C-b C-b` reaches the inner app and there is no passthrough mode for nested multiplexers.
- Mouse mode is on by default but ngmux has no drag selection; Shift+drag is the outer terminal's native selection, which knows nothing about panes.
- Config `bind` keeps only `fields[2]`, dropping command arguments.

## Scope / Constraints
- Go, `go test ./...`. Keep existing behaviour of mouse forwarding to apps that request mouse tracking, border drag resize and wheel copy-mode.
- Clipboard stays OSC 52 (works over SSH).
- Delivery strategy: ask-on-risk. Forecast ~500–700 authored lines.

## Tasks
- [x] T1 Multi-client sizing: session size = smallest attached client; recompute on attach/detach/resize/refresh; per-client mouse state. Route: delegated (multi-file server). 
- [ ] T2 Nested passthrough: F12 (and configurable `passthrough` bind) toggles forwarding of all keys incl. prefix; status bar indicator; which-key mentions `C-b C-b`. Route: delegated.
- [ ] T3 Mouse drag selection: drag in a pane without mouse-tracking app selects text clamped to that pane, release copies via OSC 52; anchor in absolute history lines. Route: delegated.
- [ ] T4 Config `bind` keeps full command line. Route: delegated with T1-T3 (single writer).

## Acceptance criteria
- Two clients of different sizes both render correctly; detaching the smaller grows the session back.
- With passthrough on, Ctrl-b reaches the pane app; F12 toggles back.
- Plain mouse drag in a shell pane copies only that pane's text to the clipboard.
- `bind S split-window -v` keeps `-v`.
- `go test ./...` and `go vet ./...` pass.

## Progress / Evidence
- T1 done (route: delegated writer). `Server.fitSessions` sizes each viewed session to min cols / min rows of its viewers; called on attach, detach, resize, refresh, session switch, new-session, session emptied. Pointer state moved to per-client `mouseState` (`client.mouse`); `session.mouse` keeps a client-less default. RED observed on old code (3 sizing tests timed out), GREEN after. Tests: `internal/server/multiclient_test.go`. Checks: go build/vet OK, `go test ./...` OK, `-race` server+client OK.

## Next step
T2.
