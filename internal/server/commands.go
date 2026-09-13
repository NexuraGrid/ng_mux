package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
)

// command is one entry in the command layer: a canonical name, optional tmux
// short aliases, and a handler. Every way to drive the multiplexer — key
// bindings, the ':' prompt, and the `ngmux <cmd>` CLI — funnels through this
// table, so adding a command touches exactly one place.
type command struct {
	name    string
	aliases []string
	// needsClient marks commands that act on the issuing client (detach,
	// session switching); they are rejected on a non-attached CLI connection.
	needsClient bool
	run         func(*cmdCtx) (string, error)
}

// cmdCtx is everything a handler needs: the server, the issuing client (nil for
// a one-shot CLI connection), the resolved target session, the pane index from
// the -t spec (-1 == active), and the remaining arguments.
type cmdCtx struct {
	srv  *Server
	cl   *client
	sess *session
	pane int
	args []string
}

// registry maps every name and alias to its command. Built once at init.
var registry = buildRegistry(commandList())

func buildRegistry(cmds []command) map[string]*command {
	m := make(map[string]*command, len(cmds)*2)
	add := func(key string, c *command) {
		if _, dup := m[key]; dup {
			panic("server: duplicate command key " + key)
		}
		m[key] = c
	}
	for i := range cmds {
		c := &cmds[i]
		add(c.name, c)
		for _, a := range c.aliases {
			add(a, c)
		}
	}
	return m
}

// commandList is the single source of truth for what commands exist.
func commandList() []command {
	return []command{
		{name: "split-window", aliases: []string{"splitw"}, run: cmdSplitWindow},
		{name: "new-window", aliases: []string{"neww"}, run: cmdNewWindow},
		{name: "kill-pane", aliases: []string{"killp"}, run: cmdKillPane},
		{name: "swap-pane", aliases: []string{"swapp"}, run: cmdSwapPane},
		{name: "break-pane", aliases: []string{"breakp"}, run: cmdBreakPane},
		{name: "join-pane", aliases: []string{"joinp"}, run: cmdJoinPane},
		{name: "kill-window", aliases: []string{"killw"}, run: cmdKillWindow},
		{name: "kill-session", run: cmdKillSession},
		{name: "next-window", aliases: []string{"next"}, run: cmdNextWindow},
		{name: "previous-window", aliases: []string{"prev"}, run: cmdPrevWindow},
		{name: "select-window", aliases: []string{"selectw"}, run: cmdSelectWindow},
		{name: "select-pane", aliases: []string{"selectp"}, run: cmdSelectPane},
		{name: "previous-pane", run: cmdPreviousPane},
		{name: "rename-window", aliases: []string{"renamew"}, run: cmdRenameWindow},
		{name: "rename-session", aliases: []string{"rename"}, run: cmdRenameSession},
		{name: "select-layout", aliases: []string{"selectl"}, run: cmdSelectLayout},
		{name: "resize-pane", aliases: []string{"resizep"}, run: cmdResizePane},
		{name: "send-keys", aliases: []string{"send"}, run: cmdSendKeys},
		{name: "display-panes", aliases: []string{"displayp"}, run: cmdDisplayPanes},
		{name: "copy-mode", run: cmdCopyMode},
		{name: "paste-buffer", run: cmdPasteBuffer},
		{name: "new-session", aliases: []string{"new"}, run: cmdNewSession},
		{name: "detach-client", aliases: []string{"detach"}, needsClient: true, run: cmdDetach},
		{name: "next-session", needsClient: true, run: cmdNextSession},
		{name: "previous-session", needsClient: true, run: cmdPrevSession},
		{name: "list-sessions", aliases: []string{"ls"}, run: cmdListSessions},
		{name: "list-windows", aliases: []string{"lsw"}, run: cmdListWindows},
		{name: "display-message", aliases: []string{"display"}, run: cmdDisplayMessage},
	}
}

// --- geometry commands (operate on the current window) ---

func cmdSplitWindow(c *cmdCtx) (string, error) {
	dir := layout.Vertical // no flag: split top / bottom, like tmux
	if hasFlag(c.args, "-h") {
		dir = layout.Horizontal
	}
	return "", c.sess.withWindow(func(w *window, cols, rows int) error {
		return w.split(c.sess, dir, cols, rows)
	})
}

func cmdNewWindow(c *cmdCtx) (string, error) {
	c.sess.mu.Lock()
	defer c.sess.mu.Unlock()
	_, err := c.sess.spawnWindow(c.sess.cols, c.sess.contentRows())
	return "", err
}

func cmdKillPane(c *cmdCtx) (string, error) {
	return "", c.sess.withWindow(func(w *window, _, _ int) error {
		if p := w.panes[w.active]; p != nil {
			// Close the pty; the output pump then ends and reports the exit
			// through the normal reap path, same as a shell that exits itself.
			p.close()
		}
		return nil
	})
}

// cmdSwapPane swaps the active pane with its previous (-U) or next (-D)
// neighbour in pane order. No flag defaults to -D.
func cmdSwapPane(c *cmdCtx) (string, error) {
	delta := +1
	if hasFlag(c.args, "-U") {
		delta = -1
	}
	return "", c.sess.withWindow(func(w *window, cols, rows int) error {
		w.swapActive(delta, cols, rows)
		return nil
	})
}

func cmdBreakPane(c *cmdCtx) (string, error) {
	return "", c.sess.breakPane()
}

// cmdJoinPane pulls the active pane of the window named by -s into the current
// window. -h splits left/right, otherwise (like split-window) top/bottom.
func cmdJoinPane(c *cmdCtx) (string, error) {
	src, _ := takeFlagValue(c.args, "-s")
	idx, err := strconv.Atoi(strings.TrimSpace(src))
	if err != nil {
		return "", fmt.Errorf("join-pane needs -s <window-index>")
	}
	dir := layout.Vertical
	if hasFlag(c.args, "-h") {
		dir = layout.Horizontal
	}
	return "", c.sess.joinPane(idx, dir)
}

func cmdKillWindow(c *cmdCtx) (string, error) {
	c.sess.mu.Lock()
	defer c.sess.mu.Unlock()
	if len(c.sess.windows) == 0 {
		return "", nil
	}
	victim := c.sess.windows[c.sess.cur]
	c.sess.removeWindowAt(c.sess.cur)
	victim.closeAll()
	return "", nil
}

func cmdKillSession(c *cmdCtx) (string, error) {
	c.srv.killSession(c.sess.name)
	return "", nil
}

func cmdNextWindow(c *cmdCtx) (string, error) {
	c.sess.stepWindow(+1)
	return "", nil
}

func cmdPrevWindow(c *cmdCtx) (string, error) {
	c.sess.stepWindow(-1)
	return "", nil
}

func cmdSelectWindow(c *cmdCtx) (string, error) {
	idx, ok := firstInt(c.args)
	if !ok {
		return "", fmt.Errorf("select-window needs a window index")
	}
	c.sess.selectWindow(idx)
	return "", nil
}

func cmdSelectPane(c *cmdCtx) (string, error) {
	return "", c.sess.withWindow(func(w *window, cols, rows int) error {
		w.unzoom(cols, rows) // moving focus makes the hidden panes matter again
		if idx, ok := firstInt(c.args); ok {
			ids := layout.Panes(w.tree)
			if idx >= 0 && idx < len(ids) {
				w.active = ids[idx]
			}
			return nil
		}
		w.focus(+1) // no index given: cycle to the next pane
		return nil
	})
}

func cmdPreviousPane(c *cmdCtx) (string, error) {
	return "", c.sess.withWindow(func(w *window, cols, rows int) error {
		w.unzoom(cols, rows)
		w.focus(-1)
		return nil
	})
}

func cmdRenameWindow(c *cmdCtx) (string, error) {
	name := strings.TrimSpace(strings.Join(c.args, " "))
	return "", c.sess.withWindow(func(w *window, _, _ int) error {
		if name != "" {
			w.name = name
		}
		return nil
	})
}

func cmdRenameSession(c *cmdCtx) (string, error) {
	return "", c.srv.renameSession(c.sess, strings.Join(c.args, " "))
}

func cmdSelectLayout(c *cmdCtx) (string, error) {
	name := layout.LayoutTiled
	if len(c.args) > 0 && c.args[0] != "" {
		name = c.args[0]
	}
	return "", c.sess.withWindow(func(w *window, cols, rows int) error {
		ids := layout.Panes(w.tree)
		if len(ids) == 0 {
			return nil
		}
		newTree := layout.Arrange(ids, name)
		if !layout.FitsMinimum(newTree, w.outer(cols, rows)) {
			return fmt.Errorf("not enough room for the %q layout", name)
		}
		w.zoom = 0
		w.tree = newTree
		w.applyLayout(cols, rows)
		return nil
	})
}

func cmdResizePane(c *cmdCtx) (string, error) {
	if hasFlag(c.args, "-Z") {
		return "", c.sess.withWindow(func(w *window, cols, rows int) error {
			w.toggleZoom(cols, rows)
			return nil
		})
	}
	orient, delta, ok := resizePaneArgs(c.args)
	if !ok {
		return "", fmt.Errorf("resize-pane needs -L, -R, -U, -D or -Z")
	}
	return "", c.sess.withWindow(func(w *window, cols, rows int) error {
		w.resizeActive(orient, delta, cols, rows)
		return nil
	})
}

func cmdSendKeys(c *cmdCtx) (string, error) {
	data := encodeKeys(c.args)
	if len(data) == 0 {
		return "", nil
	}
	var target *pane
	_ = c.sess.withWindow(func(w *window, _, _ int) error {
		ids := layout.Panes(w.tree)
		target = w.panes[w.active]
		if c.pane >= 0 && c.pane < len(ids) {
			target = w.panes[ids[c.pane]]
		}
		return nil
	})
	// Write outside the session lock: a child that is not reading its stdin
	// can block this for as long as it likes.
	if target != nil {
		_, _ = target.pt.Write(data)
	}
	return "", nil
}

// displayPanesTime is how long the per-pane index badges stay up, matching
// tmux's default display-panes-time.
const displayPanesTime = time.Second

func cmdDisplayPanes(c *cmdCtx) (string, error) {
	c.sess.mu.Lock()
	c.sess.displayPanesUntil = time.Now().Add(displayPanesTime)
	c.sess.mu.Unlock()
	return "", nil
}

func cmdCopyMode(c *cmdCtx) (string, error) {
	return "", c.sess.withWindow(func(w *window, cols, rows int) error {
		w.enterCopy(cols, rows)
		return nil
	})
}

func cmdPasteBuffer(c *cmdCtx) (string, error) {
	var target *pane
	c.sess.mu.Lock()
	buf := c.sess.pasteBuf
	if w := c.sess.current(); w != nil && buf != "" {
		target = w.panes[w.active]
	}
	c.sess.mu.Unlock()
	// Write outside the session lock, like send-keys: a large paste into a
	// child that is not reading would otherwise freeze the session.
	if target != nil {
		_, _ = target.pt.Write([]byte(buf))
	}
	return "", nil
}

// --- client / server scoped commands ---

func cmdDetach(c *cmdCtx) (string, error) {
	c.cl.send(protocol.Message{Type: protocol.TypeBye})
	return "", nil
}

// cmdNewSession creates a session and, when issued from an attached client,
// moves that client into it. Forms: `new-session`, `new-session -s NAME`,
// `new-session NAME`. Without a name the next free integer is used, like tmux.
func cmdNewSession(c *cmdCtx) (string, error) {
	name := sessionNameArg(c.args)
	explicit := name != ""
	if !explicit {
		name = c.srv.nextSessionName()
	} else if c.srv.hasSession(name) {
		return "", fmt.Errorf("session %q already exists", name)
	}

	sess, err := c.srv.getOrCreateSession(name)
	if err != nil {
		return "", err
	}
	if c.cl != nil {
		if cols, rows := c.cl.size(); cols > 0 && rows > 0 {
			sess.resize(cols, rows)
		}
		c.cl.setSession(sess.name)
	}
	return "", nil
}

// sessionNameArg pulls a session name out of a new-session arg list: the value
// after -s or -t, else the first token that is not a flag.
func sessionNameArg(args []string) string {
	if v, _ := takeFlagValue(args, "-s"); v != "" {
		return v
	}
	if v, _ := takeFlagValue(args, "-t"); v != "" {
		return v
	}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

func cmdNextSession(c *cmdCtx) (string, error) {
	c.srv.switchClientSession(c.cl, true)
	return "", nil
}

func cmdPrevSession(c *cmdCtx) (string, error) {
	c.srv.switchClientSession(c.cl, false)
	return "", nil
}

func cmdListSessions(c *cmdCtx) (string, error) { return c.srv.listSessionsText(), nil }

func cmdListWindows(c *cmdCtx) (string, error) { return c.sess.listWindowsText(), nil }

func cmdDisplayMessage(c *cmdCtx) (string, error) { return strings.Join(c.args, " "), nil }

// resizePaneArgs reads -L/-R/-U/-D plus an optional cell count and returns the
// axis and signed delta for window.resizeActive. ok is false when no direction
// flag is present.
func resizePaneArgs(args []string) (orient layout.Orientation, delta int, ok bool) {
	step := resizeStep
	if v, has := firstInt(args); has {
		step = v
	}
	switch {
	case hasFlag(args, "-L"):
		return layout.Horizontal, -step, true
	case hasFlag(args, "-R"):
		return layout.Horizontal, step, true
	case hasFlag(args, "-U"):
		return layout.Vertical, -step, true
	case hasFlag(args, "-D"):
		return layout.Vertical, step, true
	}
	return layout.Leaf, 0, false
}
