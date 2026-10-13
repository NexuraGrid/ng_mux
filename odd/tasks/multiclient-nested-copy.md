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
- [x] T2 Nested passthrough: F12 (and configurable `passthrough` bind) toggles forwarding of all keys incl. prefix; status bar indicator; which-key mentions `C-b C-b`. Route: delegated.
- [x] T3 Mouse drag selection: drag in a pane without mouse-tracking app selects text clamped to that pane, release copies via OSC 52; anchor in absolute history lines. Route: delegated.
- [x] T4 Config `bind` keeps full command line. Route: delegated with T1-T3 (single writer).

## Acceptance criteria
- Two clients of different sizes both render correctly; detaching the smaller grows the session back.
- With passthrough on, Ctrl-b reaches the pane app; F12 toggles back.
- Plain mouse drag in a shell pane copies only that pane's text to the clipboard.
- `bind S split-window -v` keeps `-v`.
- `go test ./...` and `go vet ./...` pass.

## Progress / Evidence
- T1 done, commit `9869e1b` (route: delegated writer). `Server.fitSessions` sizes each viewed session to min cols / min rows of its viewers; called on attach, detach, resize, refresh, session switch, new-session, session emptied. Pointer state moved to per-client `mouseState` (`client.mouse`); `session.mouse` keeps a client-less default. RED observed on old code (3 sizing tests timed out), GREEN after. Tests: `internal/server/multiclient_test.go`. Checks: go build/vet OK, `go test ./...` OK, `-race` server+client OK.

- T2 done, commit `1b0fa8b` (route: delegated writer). Client `forwardInput` keeps a passthrough flag toggled by F12 (`\x1b[24~`, intercepted in `escapeKey`), a `passthrough` bind, or `:passthrough`; while on the prefix is forwarded like any byte. New `protocol.TypePassthrough` (N=1/0) tells the server, which stamps a per-client `PASSTHROUGH · F12` pill on a copy of the shared frame (`render.StampStatusRight`). Which-key/session help and README updated. RED observed (4 client tests failed, server test did not compile), GREEN after. Tests: `internal/client/passthrough_test.go`, `TestPassthroughMarksOnlyThatClientsStatusBar`. Checks: go build/vet OK, `go test ./...` OK, `-race` server+client OK.

- T3 done, commit `0a1e75f` (route: delegated writer). Left press on a pane without mouse tracking (or already in copy-mode) arms a per-client drag selection; first move off the pressed cell enters copy-mode on that pane and anchors there; drag is clamped to the pane rect (`localCoords`); release copies (`copyState.selectedText`), stores the paste buffer, sends OSC 52 via `Server.sendClipboard`, exits copy-mode. Click without drag only focuses. Held-button drag/release events are clamped into the content area so a release on the status row still ends the gesture. Copy-mode anchor now stored as an absolute line (`aLine`, ScrolledTotal-based) so it survives scrolling; extraction reads a scrollback window spanning both ends. RED observed (2 drag tests + anchor test failed), GREEN after. Tests: `internal/server/mouse_select_test.go`. Existing TestDragForwardingRespectsTrackingMode / TestBorderDragStillResizesPanes / TestWheelOnPlainShellEntersCopyMode green. Checks: go build/vet OK, `go test ./...` OK, `-race` server+client OK.

- T4 done, commit `e8bb8ac` (route: delegated writer). `bind` keeps `strings.Join(fields[2:], " ")`; README config example shows `bind S split-window -v` and `bind P passthrough`. RED observed (S and R binds truncated), GREEN after. Test: `TestBindKeepsFullCommandLine`. Checks: go build/vet OK, `go test ./...` OK, `-race` server+client OK.

## Next step
All tasks done. Native review (medium risk, consent granted, 1 reliability lens) approved and acknowledged, lineage `review-b3c2494532047832`. Advisory follow-ups (non-blocking): R3-fit-race (fitSessions unlocks before resize), R3-resize-no-markdirty, R3-prompt-trim, R3-switch-mouse-reset-partial, R3-vacuous-perclient-test; R3-stamp-narrow-oob checked: false positive, `Frame.at/set` are bounds-checked. Pending: push, PR and release (human decisions).
