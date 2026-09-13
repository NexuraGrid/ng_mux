package server

import (
	"errors"
	"fmt"
	"io"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/ptyx"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// pty is the pseudo-terminal half of a pane: a child process on a real pty.
// *ptyx.Pane is the production implementation; tests substitute an in-memory
// fake so session/window logic can run without spawning a shell.
type pty interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// screen is the terminal-emulator half of a pane: it consumes pty output and
// answers snapshot/scrollback queries. *vterm.Term is the production
// implementation.
type screen interface {
	io.Writer
	Resize(cols, rows int)
	Snapshot() vterm.Snapshot
	// SnapshotInto is Snapshot writing into a caller-owned value so the hot
	// render path can reuse one buffer per pane instead of allocating.
	SnapshotInto(dst *vterm.Snapshot)
	ScrollbackView(offset, rows int) vterm.Snapshot
	HistoryLen() int
	SetHistoryLimit(n int)
	// Dirty reports whether bytes have arrived (or a resize happened) since the
	// last Snapshot. The broadcaster uses it to skip idle sessions entirely.
	Dirty() bool
	// ScrolledTotal is the number of lines ever pushed into history (never
	// reset by eviction). copyState.sync uses it to keep an anchored
	// copy-mode view fixed while more output arrives.
	ScrolledTotal() uint64
	// InputModes reports the app's current mouse/alt-screen/app-cursor modes,
	// used to decide whether a mouse event should be forwarded to the pane's
	// own app instead of driving copy-mode.
	InputModes() vterm.InputModes
}

// paneFactory builds a pane for a window. Injected through sessionOpts so tests
// can supply fakes; production always uses startPane.
type paneFactory func(id layout.PaneID, cols, rows int, shell string, histLimit int) (*pane, error)

// pane couples a child process on a pty with the emulator that interprets its
// output. One pane == one shell. win is the window that currently holds it, so
// an exit event can be routed back to the right split tree. copy is non-nil
// while the pane is in scrollback / selection mode.
type pane struct {
	id   layout.PaneID
	win  *window
	pt   pty
	vt   screen
	copy *copyState

	// logf reports unusual daemon-side conditions tied to this pane (a
	// recovered emulator panic, a pump goroutine that itself panicked) so
	// they are visible without crashing anything to surface them. Set by the
	// window/session that owns the pane from Server.log.Printf; nil-safe, so
	// tests that build a *pane by hand can leave it unset.
	logf func(format string, args ...any)
}

// startPane opens a pty running shell (empty = platform default) and wires an
// emulator with the given scrollback limit. It is the production paneFactory.
func startPane(id layout.PaneID, cols, rows int, shell string, histLimit int) (*pane, error) {
	pt, err := ptyx.Start(ptyx.Config{Cols: cols, Rows: rows, Prog: shell})
	if err != nil {
		return nil, err
	}
	vt := vterm.New(cols, rows, pt)
	vt.SetHistoryLimit(histLimit)
	return &pane{id: id, pt: pt, vt: vt}, nil
}

// maxWriteChunk bounds how many bytes pump feeds the emulator per vt.Write
// call. vterm.Term.Write holds the emulator's lock for the whole call, and
// the server's single broadcast goroutine needs that lock (via Dirty-free
// polling plus SnapshotInto) to render every session's frame; a pane emitting
// a full 32 KiB pty read in one Write would hold the lock for however long
// that takes to parse, delaying every other pane and session behind it.
// Splitting into smaller slices lets the lock be released and re-acquired
// between them, so a snapshot queued behind a big burst gets in promptly
// instead of waiting for the whole burst. vterm.Term.Write carries an
// incomplete trailing UTF-8 rune across calls (see its pending field), so
// slicing here at an arbitrary byte boundary can never corrupt a multi-byte
// character even if it lands mid-rune.
const maxWriteChunk = 4096

// pump copies pty output into the emulator until the child exits or errors.
// It calls onExit exactly once when the pane's process is finished.
//
// The emulator itself recovers from a panic and reports it as an error
// wrapping vterm.ErrEmulatorPanic (see vterm.Term.Write), so the ordinary case
// here is just logging that and continuing to pump: one pane's malformed
// escape sequence must never stop its shell from being usable, let alone take
// the daemon down. The deferred recoverAndLog is a last resort for anything
// pump does outside that guarded call (reading the pty, invoking onExit) —
// it should never fire in practice, but if it does, this goroutine dying
// quietly is far better than the whole process dying loudly.
func (p *pane) pump(onExit func(*pane)) {
	defer recoverAndLog(p.logf, fmt.Sprintf("pane %d pump", p.id))
	buf := make([]byte, 32*1024)
	for {
		n, err := p.pt.Read(buf)
		if n > 0 {
			p.feed(buf[:n])
		}
		if err != nil {
			break
		}
	}
	onExit(p)
}

// feed writes data into the emulator in slices of at most maxWriteChunk bytes
// (see its doc comment for why) and logs a recovered emulator panic exactly
// as pump always has.
func (p *pane) feed(data []byte) {
	for len(data) > 0 {
		chunk := data
		if len(chunk) > maxWriteChunk {
			chunk = chunk[:maxWriteChunk]
		}
		if _, werr := p.vt.Write(chunk); werr != nil && errors.Is(werr, vterm.ErrEmulatorPanic) {
			if p.logf != nil {
				p.logf("pane %d: %v", p.id, werr)
			}
		}
		data = data[len(chunk):]
	}
}

func (p *pane) resize(cols, rows int) {
	p.vt.Resize(cols, rows)
	_ = p.pt.Resize(cols, rows)
	if p.copy != nil {
		p.copy.rows, p.copy.cols = rows, cols
		if p.copy.cy >= rows {
			p.copy.cy = rows - 1
		}
	}
}

// syncCopy re-anchors the pane's copy-mode offset (see copyState.sync) against
// however many lines have been pushed into history since it was last synced.
// A no-op when the pane is not in copy-mode.
func (p *pane) syncCopy() {
	if p.copy != nil {
		p.copy.sync(p.vt.ScrolledTotal(), p.vt.HistoryLen())
	}
}

// copyKey feeds one key chunk to copy-mode. It returns the text to store in the
// paste buffer (empty unless the key was a yank) and whether copy-mode ended.
func (p *pane) copyKey(data []byte) (yank string, exited bool) {
	cs := p.copy
	if cs == nil {
		return "", false
	}
	p.syncCopy()
	exit, doYank := cs.key(data, p.vt.HistoryLen())
	if doYank {
		snap := p.vt.ScrollbackView(cs.offset, cs.rows)
		yank = extractText(snap, cs.ax, cs.ay, cs.cx, cs.cy)
	}
	if exit {
		p.copy = nil
	}
	return yank, exit
}

func (p *pane) close() {
	_ = p.pt.Close()
}
