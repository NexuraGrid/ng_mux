# ngmux

A small, cross-platform terminal multiplexer written in pure Go. Runs on Linux,
macOS and Windows from the same codebase.

On Windows it uses **ConPTY** where available (Windows 10 1809+ / Server 2019+).
Older builds (Windows Server 2016 and earlier) have no ConPTY, so ngmux falls
back to **winpty**: its native helpers are embedded in the executable and
unpacked to `%LOCALAPPDATA%\ngmux\` on first run — nothing extra to install.

Like tmux, it is **client/server**: a background daemon owns every session,
pane, pseudo-terminal and terminal emulator; thin client processes attach to it,
forward keystrokes, and paint the ANSI frames the daemon streams back. Detaching
just kills the client — the daemon and your shells keep running.

## Status: Phase 4

Working now:

- background daemon, auto-spawned on first `ngmux`
- **multiple sessions**, each with **multiple windows**, each a tree of panes
  (`ngmux new -s NAME`, `Ctrl-b : new-session -s NAME`, `Ctrl-b m` for the
  session cheat-sheet)
- split a pane left/right or top/bottom; **resize panes** with the keyboard
- move focus between panes; cycle / select / kill windows
- switch between sessions while attached
- named sessions: `ngmux new -s work`, `ngmux attach -t work`
- **scrollback + copy-mode**: scroll history, select text, yank to a paste
  buffer, paste it back (and to the OS clipboard via OSC 52 — `set set-clipboard off` to opt out)
- **zoom** a pane full-screen (`Ctrl-b z`), **flash pane numbers** (`Ctrl-b q`),
  **swap** / **break** / **join** panes (`Ctrl-b {` `}` `!`, `join-pane -s N`)
- **double-width characters** (CJK, emoji) render across two columns in panes and the status bar
- **config file** (`~/.config/ngmux/ngmux.conf`): custom prefix key, key
  bindings, history limit, default shell, status-bar colours
- **command layer**: `ngmux <command> [args]` scripts the running server
  (`new-window`, `split-window -h`, `send-keys`, `select-layout`, `rename-*`, …),
  and `Ctrl-b :` opens the same commands as an in-terminal prompt
- **preset layouts**: `select-layout even-horizontal | even-vertical | tiled |
  main-vertical | main-horizontal`
- **rename** windows and sessions
- **mouse** (on by default; `set mouse off` to disable): click a pane to focus
  it, drag a border to resize, wheel to scroll history, click a window name or
  the `[+]` button in the status bar
- status bar: session name, window list with the active marker, clock
- kill a pane; detach and re-attach
- `ngmux ls`, `ngmux kill-session -t name`, `ngmux kill-server`
- cross-platform PTY (real pty on Unix; ConPTY or embedded winpty on Windows)
  and IPC (Unix socket / named pipe)

Deliberately out of scope: hooks, session groups, `choose-tree`,
system-clipboard integration.

### Scrollback caveat

vt10x keeps no history of its own, so ngmux reconstructs scrollback by feeding
the emulator line by line and detecting when the screen scrolls. This is
reliable for shell output; capture is skipped entirely while a full-screen app
(vim, less, htop) holds the alternate screen, matching tmux.

## Install

**Linux / macOS** — one line, no toolchain needed:

```sh
curl -fsSL https://raw.githubusercontent.com/NexuraGrid/ng_mux/main/install.sh | sh
```

It drops the `ngmux` binary in `~/.local/bin` (override with `NGMUX_INSTALL_DIR`)
and adds that directory to your shell's `PATH` if it is missing.

**Windows** — from any shell (cmd, PowerShell, Nushell, Git Bash):

```powershell
powershell -c "irm https://raw.githubusercontent.com/NexuraGrid/ng_mux/main/install.ps1 | iex"
```

Already in a PowerShell prompt? `irm …/install.ps1 | iex` on its own works too.
Prefer a package? `go install github.com/MauricioJC3/ng_mux/cmd/ngmux@latest`
(needs Go 1.27+) drops `ngmux.exe` in `~\go\bin`.

**Update** to the latest release at any time:

```sh
ngmux update
```

`ngmux version` prints the installed version.

### Build from source

```sh
go install github.com/MauricioJC3/ng_mux/cmd/ngmux@latest   # needs Go 1.27+
# or, from a clone:
go build -o ngmux ./cmd/ngmux       # native
GOOS=windows go build ./cmd/ngmux   # Windows binary
```

## Use

```
ngmux [new] [-s name]      start the server if needed, then attach
ngmux attach | a [-t name] attach to a running server
ngmux ls                   list sessions
ngmux kill-session -t name kill one session
ngmux kill-server          stop the server
ngmux update               install the latest release
ngmux version              print the installed version
ngmux <command> [args]     run a command on the running server:
  ngmux new-window
  ngmux split-window -h
  ngmux send-keys -t work "make test" Enter
  ngmux select-layout tiled
  ngmux rename-window build
  ngmux rename-session prod
  ngmux list-windows
```

Targets: `-t <session>`, `-t <session>:<window>`, `-t <session>:<window>.<pane>`
(pane and window are indices). `send-keys` arguments are key names (`Enter`,
`Space`, `C-c`, `Up`, …) or literal strings.

### Sessions

A session is a workspace that outlives the client. Detach and its shells (and
whatever they are running) keep going in the daemon; re-attach later, from
anywhere.

```
ngmux new -s work            create "work" and attach
ngmux new -s logs            create a second session
ngmux ls                     list them: name, windows, panes, (attached)
ngmux attach -t work         jump back into one
ngmux kill-session -t logs   stop one session and its shells
```

While attached: `Ctrl-b (` / `Ctrl-b )` move between sessions, `Ctrl-b :
new-session -s NAME` creates one without leaving, `Ctrl-b : rename-session NAME`
renames the current one, `Ctrl-b d` detaches, and `Ctrl-b m` pops up this list.
An unnamed session gets the next free number (`0`, `1`, `2`, …), like tmux.

### Key bindings

Prefix is **Ctrl-b** (same as tmux).

| Keys            | Action                     |
|-----------------|----------------------------|
| `Ctrl-b "`      | split top / bottom         |
| `Ctrl-b %`      | split left / right         |
| `Ctrl-b o`      | focus next pane            |
| `Ctrl-b ;`      | focus previous pane        |
| `Ctrl-b →/↓/←/↑`| focus next / previous pane |
| `Ctrl-b H/J/K/L`| resize pane left/down/up/right |
| `Ctrl-b z`      | zoom focused pane (toggle full screen) |
| `Ctrl-b q`      | flash each pane's index    |
| `Ctrl-b {` / `}`| swap focused pane with previous / next |
| `Ctrl-b !`      | break focused pane into its own window |
| `Ctrl-b x`      | kill focused pane          |
| `Ctrl-b c`      | new window                 |
| `Ctrl-b n` / `p`| next / previous window     |
| `Ctrl-b 0`–`9`  | select window by index     |
| `Ctrl-b &`      | kill window                |
| `Ctrl-b (` / `)`| previous / next session    |
| `Ctrl-b m`      | session cheat-sheet (new / attach / list) |
| `Ctrl-b [`      | enter copy-mode (scrollback) |
| `Ctrl-b ]`      | paste the paste buffer     |
| `Ctrl-b :`      | command prompt             |
| `Ctrl-b d`      | detach                     |
| `Ctrl-b Ctrl-b` | send a literal Ctrl-b      |

In copy-mode: arrows / PageUp / PageDown move and scroll, `g` / `G` jump to the
oldest / newest line, `Space` starts a selection, `Enter` or `y` copies it and
exits, `Esc` or `q` exits.

To pull a pane back in from another window, `Ctrl-b : join-pane -s N` (add `-h`
for a left/right split); it is the inverse of `Ctrl-b !`.

### Config file

`~/.config/ngmux/ngmux.conf` (or `%APPDATA%\ngmux\ngmux.conf`; override with
`NGMUX_CONFIG`). One directive per line:

```
set prefix C-a
set history-limit 5000
set default-shell /bin/bash
set escape-time 25
set status-fg 0
set status-bg 4
set set-clipboard on
bind s split-vertical
bind v split-horizontal
```

`escape-time` is how many milliseconds a lone `Esc` waits for the rest of an
escape sequence before it is sent on its own (default 25). Raise it on a slow
link if arrow keys misfire; lower it to `0` if an app inside ngmux feels
sluggish to react to `Esc`.

`set-clipboard` (default `on`) also copies a copy-mode yank to the system
clipboard using an OSC 52 escape, so `Ctrl-b ]` is not the only way to get it
back. Turn it `off` if your terminal does not support OSC 52 or you would
rather ngmux never touch the clipboard.

Unknown lines are logged and skipped, never fatal.

### Troubleshooting

The daemon always logs to `<user cache dir>/ngmux/ngmuxd-<session>.log`
(`~/.cache/ngmux/` on Linux, `~/Library/Caches/ngmux/` on macOS,
`%LocalAppData%\ngmux\` on Windows) — the file is rotated (truncated) once it
passes 5 MiB. Set `NGMUX_DEBUG=1` before starting the daemon to also mirror
that output to stderr.

## Architecture

```
cmd/ngmux            CLI + daemon spawn (build-tagged per OS)
internal/
  protocol          length-prefixed JSON framing, client<->server messages
  ipc               transport: Unix domain socket / Windows named pipe
  config            ngmux.conf parser (prefix, binds, limits, colours)
  ptyx              pseudo-terminal wrapper (go-pty; embedded winpty fallback)
  vt10x             in-tree fork of hinshun/vt10x, the VT100/xterm emulator
  vterm             vt10x wrapper: raw bytes in, cell snapshot out, + scrollback
  layout            binary split tree -> pane rectangles; split / remove / resize
  render            composite panes + borders + status bar, ANSI frame diffing
  termio            client's real terminal: raw mode, size, resize events
  server            the daemon: sessions -> windows -> panes, broadcast loop
    session.go        one named session: ordered windows, current window, status
    window.go         one window: the pane split tree and focus
    copymode.go       per-pane scrollback / selection state machine
    command.go        free-form command line parser + dispatcher
    mouse.go          click-to-focus, border drag-resize, wheel-scroll
  client            the thin half: connect, raw mode, forward input, blit frames
                     (also parses mouse sequences and the ':' prompt)
  e2e               daemon + client driven through a real pty
```

## Test

```sh
go test ./...
```

`internal/e2e` starts an in-process daemon, attaches a client over a real pty,
and checks that shell output reaches the screen, that splitting adds a pane, and
that detach stops the client.
