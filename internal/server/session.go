package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/layout"
	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/ptyx"
	"github.com/MauricioJC3/ng_mux/internal/render"
	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// sessionOpts carries the configuration a session needs at creation time.
type sessionOpts struct {
	historyLimit int
	defaultShell string // empty => platform default
	setClipboard bool   // mirror copy-mode yanks to the OS clipboard (OSC 52)

	// palette styles the status bar, borders, pane titles, badges and the
	// copy-mode indicator. The zero value means theme.Dark().
	palette theme.Palette
	// paneTitles draws a title line above each pane of a split window.
	paneTitles bool

	// newPane builds a pane. Nil means the production factory (startPane);
	// tests inject a fake so session/window logic runs without a real shell.
	newPane paneFactory

	// logf reports unusual daemon-side conditions (a recovered emulator
	// panic, a goroutine that had to be saved by recoverAndLog) without
	// crashing anything to surface them. Filled in by newServer from
	// Server.log.Printf; nil-safe, so it can be left unset in tests that
	// don't care about this output.
	logf func(format string, args ...any)
}

// session is a named workspace holding an ordered list of windows, one of
// which is current. All state is guarded by mu; window and pane methods are
// only ever called with it held.
type session struct {
	mu sync.Mutex

	name    string
	windows []*window
	cur     int
	created time.Time
	opts    sessionOpts

	nextPaneID   layout.PaneID
	nextWindowID int

	cols, rows int // viewport of the attached client(s); rows includes status

	pasteBuf   string      // last copy-mode yank; target of the paste command
	drag       dragState   // in-progress mouse border drag
	statusHits []statusHit // clickable status-bar regions, rebuilt each frame

	// tabScratch / tabWidths are buildStatus's per-frame tab texts and their
	// display widths, kept so a steady repaint does not regrow them.
	tabScratch []string
	tabWidths  []int

	// mouseFwd is the pane a press started forwarding mouse reports to (0
	// when no forwarded press is in progress). It is set by routePress and
	// consulted by routeDrag/routeRelease so a drag or release that strays
	// off the pane's rectangle (or off any pane at all) still reaches the
	// same app the press did, matching how a real terminal tracks a button
	// once it goes down.
	mouseFwd layout.PaneID

	// needsRepaint forces the next frame even if no pane produced output
	// (a command changed focus, layout, a window name, ...). frame() clears it.
	needsRepaint bool

	// displayPanesUntil is the wall-clock instant the per-pane index badges
	// (display-panes) stop showing. panesShown records whether the last frame
	// drew them, so dirty() can ask for exactly one more repaint to clear them.
	displayPanesUntil time.Time
	panesShown        bool

	// Render scratch, touched only by frame() on the single broadcast
	// goroutine: two frames ping-ponged so a steady repaint allocates nothing,
	// plus reused pane-view and snapshot buffers.
	frameBuf    [2]*render.Frame
	frameIdx    int
	viewScratch []render.PaneView
	snapScratch []vterm.Snapshot

	onEmpty  func(name string) // called once when the last window is gone
	paneExit chan *pane
	dead     chan struct{}
	deadOnce sync.Once
}

func newSession(name string, cols, rows int, opts sessionOpts, onEmpty func(string)) (*session, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if opts.newPane == nil {
		opts.newPane = startPane
	}
	if opts.palette == (theme.Palette{}) {
		opts.palette = theme.Dark()
	}
	s := &session{
		name:     name,
		created:  time.Now(),
		opts:     opts,
		cols:     cols,
		rows:     rows,
		onEmpty:  onEmpty,
		paneExit: make(chan *pane, 32),
		dead:     make(chan struct{}),
	}
	if _, err := s.spawnWindow(cols, s.contentRows()); err != nil {
		return nil, err
	}
	go s.reap()
	return s, nil
}

// spawnWindow creates a window, starts its pane's output pump, appends it to
// the session, and makes it current. It is the single path for adding a
// window: session creation, the new-window command, and the status-bar [+]
// button all go through here. Caller holds mu (except during newSession, where
// the session is not yet shared).
func (s *session) spawnWindow(cols, rows int) (*window, error) {
	w, err := newWindow(s.nextWin(), s.defaultWindowName(), s.nextPane(), cols, rows,
		s.opts.defaultShell, s.opts.historyLimit, s.opts.newPane, s.opts.logf)
	if err != nil {
		return nil, err
	}
	w.titles = s.opts.paneTitles
	for _, p := range w.panes {
		go p.pump(s.reportExit)
	}
	s.windows = append(s.windows, w)
	s.cur = len(s.windows) - 1
	return w, nil
}

// breakPane moves the current window's active pane into a new window of its
// own, keeping its shell running. It is a no-op error when the window has just
// one pane.
func (s *session) breakPane() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.current()
	if w == nil {
		return nil
	}
	cols, rows := s.cols, s.contentRows()
	p := w.detachPane(w.active, cols, rows)
	if p == nil {
		return fmt.Errorf("break-pane: the window has only one pane")
	}
	nw := wrapWindow(s.nextWin(), s.defaultWindowName(), p, s.opts.defaultShell,
		s.opts.historyLimit, s.opts.newPane, s.opts.logf)
	nw.titles = s.opts.paneTitles
	s.windows = append(s.windows, nw)
	s.cur = len(s.windows) - 1
	nw.applyLayout(cols, rows)
	return nil
}

// joinPane pulls the active pane of window srcIdx into the current window,
// splitting the current active pane along dir. If the source window is left
// empty it is removed. The room check happens before any pane moves, so a
// join that will not fit changes nothing.
func (s *session) joinPane(srcIdx int, dir layout.Orientation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst := s.current()
	if dst == nil {
		return nil
	}
	if srcIdx < 0 || srcIdx >= len(s.windows) {
		return fmt.Errorf("join-pane: no window %d", srcIdx)
	}
	src := s.windows[srcIdx]
	if src == dst {
		return fmt.Errorf("join-pane: source and target are the same window")
	}
	p := src.panes[src.active]
	if p == nil {
		return fmt.Errorf("join-pane: source window has no pane")
	}
	cols, rows := s.cols, s.contentRows()
	if !layout.CanSplit(dst.tree, dst.active, dir, dst.outerFor(cols, rows, len(dst.panes)+1)) {
		return fmt.Errorf("join-pane: not enough room to split the target window")
	}

	lastInSrc := len(src.panes) == 1
	if lastInSrc {
		delete(src.panes, p.id)
		src.tree = nil
	} else {
		src.detachPane(p.id, cols, rows)
	}
	if err := dst.adoptPane(p, dir, cols, rows); err != nil {
		return err // unreachable: the room check above already passed
	}
	if lastInSrc {
		s.removeWindow(src)
	}
	for i, w := range s.windows {
		if w == dst {
			s.cur = i
			break
		}
	}
	return nil
}

func (s *session) defaultWindowName() string {
	sh := s.opts.defaultShell
	if sh == "" {
		sh = ptyx.DefaultShell()
	}
	return strings.TrimSuffix(filepath.Base(sh), ".exe")
}

func (s *session) nextPane() layout.PaneID {
	s.nextPaneID++
	return s.nextPaneID
}

func (s *session) nextWin() int {
	id := s.nextWindowID
	s.nextWindowID++
	return id
}

func (s *session) contentRows() int {
	if s.rows <= 1 {
		return s.rows
	}
	return s.rows - 1
}

func (s *session) reportExit(p *pane) {
	select {
	case s.paneExit <- p:
	case <-s.dead:
	}
}

// reap folds finished panes out of the layout and, when a window loses its
// last pane, drops the window. When the session loses its last window it
// notifies the server and stops.
func (s *session) reap() {
	for {
		select {
		case <-s.dead:
			return
		case p := <-s.paneExit:
			if s.reapOne(p) {
				if s.onEmpty != nil {
					s.onEmpty(s.name)
				}
				return
			}
		}
	}
}

// reapOne processes one exited pane's cleanup and reports whether the session
// is now empty (reap should stop). It is split out of reap, with its own
// deferred recover, so a panic while folding the pane out of the layout is
// logged and this exit is skipped instead of killing the reap goroutine —
// which would silently stop reaping every future exit in this session,
// leaking every pane that exits afterward. s.mu is released via defer rather
// than an explicit unlock so a recovered panic can never leave it held.
func (s *session) reapOne(p *pane) (empty bool) {
	defer recoverAndLog(s.opts.logf, "session reap")
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := p.win; w != nil {
		w.removePane(p.id, s.cols, s.contentRows())
		if len(w.panes) == 0 {
			s.removeWindow(w)
		}
		// The tree changed off the command path, so nothing else has marked
		// the session dirty: force the next frame so the client drops the
		// closed pane instead of waiting for the survivor to emit output.
		s.needsRepaint = true
	}
	return len(s.windows) == 0
}

// removeWindow drops w from the list by identity and clamps cur. Caller holds mu.
func (s *session) removeWindow(w *window) {
	for i, x := range s.windows {
		if x == w {
			s.removeWindowAt(i)
			return
		}
	}
}

func (s *session) removeWindowAt(i int) {
	s.windows = append(s.windows[:i:i], s.windows[i+1:]...)
	if s.cur >= len(s.windows) {
		s.cur = len(s.windows) - 1
	}
	if s.cur < 0 {
		s.cur = 0
	}
}

func (s *session) resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols <= 0 || rows <= 0 || (cols == s.cols && rows == s.rows) {
		return
	}
	s.cols, s.rows = cols, rows
	content := s.contentRows()
	for _, w := range s.windows {
		w.applyLayout(cols, content)
	}
}

// input routes raw bytes: to the focused pane's copy-mode scroller if it is in
// copy-mode, otherwise straight to its pty. repaint is true when the client
// should be sent a full repaint (copy-mode navigation moves the whole view).
// yanked is the text a copy-mode yank just produced, for the caller to mirror
// to the OS clipboard (empty otherwise).
func (s *session) input(data []byte) (repaint bool, yanked string) {
	p, inCopy, text := s.routeInput(data)
	if inCopy {
		return true, text
	}
	if p != nil {
		_, _ = p.pt.Write(data)
	}
	return false, ""
}

// routeInput is input's locked half: it feeds data to copy-mode when the
// focused pane is in it, and otherwise returns the pane whose pty should get
// the bytes. The pty write stays outside the lock. mu is released with defer so
// a recovered panic in copy-mode handling can never leave the session locked.
func (s *session) routeInput(data []byte) (p *pane, inCopy bool, yanked string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.current(); w != nil {
		p = w.panes[w.active]
	}
	if p == nil || p.copy == nil {
		return p, false, ""
	}
	text, _ := p.copyKey(data)
	if text != "" {
		s.pasteBuf = text
	}
	return p, true, text
}

// current returns the current window, or nil if the session has none. Caller
// holds mu.
func (s *session) current() *window {
	if len(s.windows) == 0 {
		return nil
	}
	if s.cur < 0 || s.cur >= len(s.windows) {
		s.cur = 0
	}
	return s.windows[s.cur]
}

// withWindow runs fn with the session locked and the current window plus its
// content-area size. It is the common preamble for command handlers that act on
// the current window; a session with no window is a silent no-op.
func (s *session) withWindow(fn func(w *window, cols, contentRows int) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.current()
	if w == nil {
		return nil
	}
	return fn(w, s.cols, s.contentRows())
}

// selectWindow makes window idx current, if idx is in range.
func (s *session) selectWindow(idx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx >= 0 && idx < len(s.windows) {
		s.cur = idx
	}
}

// stepWindow moves the current-window pointer by delta, wrapping around.
func (s *session) stepWindow(delta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.windows); n > 0 {
		s.cur = (s.cur + delta%n + n) % n
	}
}

// frame renders the current window plus the status bar into one of the
// session's two reusable frame buffers. It clears needsRepaint: after this call
// the session is "clean" until something changes again. Only ever called from
// the broadcast goroutine.
func (s *session) frame() *render.Frame {
	cols, rows, views, status := s.frameInputs()
	f := render.ComposeThemedInto(s.frameBuf[s.frameIdx], cols, rows, views, status, &s.opts.palette)
	s.frameBuf[s.frameIdx] = f
	s.frameIdx ^= 1
	return f
}

// frameInputs is frame's locked half: it snapshots the panes and builds the
// status bar under mu, released with defer so a recovered panic while building
// a view can never leave the session locked (which would freeze its input and
// every later frame).
func (s *session) frameInputs() (cols, rows int, views []render.PaneView, status []render.StatusSegment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.needsRepaint = false
	cols, rows = s.cols, s.rows
	content := s.contentRows()
	showNums := time.Now().Before(s.displayPanesUntil)
	s.panesShown = showNums
	if w := s.current(); w != nil {
		views, s.snapScratch = w.views(cols, content, showNums, s.viewScratch[:0], s.snapScratch)
		s.viewScratch = views
	}
	status = s.buildStatus(cols)
	return cols, rows, views, status
}

// dirty reports whether the next frame would differ from the last one frame()
// produced: a pending needsRepaint, or a pane that consumed new output.
func (s *session) dirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.needsRepaint {
		return true
	}
	// While the display-panes badges are up, repaint every tick; once they
	// expire, ask for one final repaint to wipe them.
	if time.Now().Before(s.displayPanesUntil) || s.panesShown {
		return true
	}
	// Only what is drawn can change the next frame: the current window, or just
	// its zoomed pane. frame() clears the flag only on the panes it snapshots,
	// so counting hidden panes kept a session with a busy (or merely fresh)
	// background window dirty forever, recomposing every tick. Showing a hidden
	// pane (select-window, unzoom, a status click) marks the session itself.
	// A hidden window whose activity mark is stale needs one repaint to show
	// (or drop) it; after that activityShown matches and it costs nothing.
	for i, hw := range s.windows {
		if i != s.cur && hw.hasOutput() != hw.activityShown {
			return true
		}
	}
	w := s.current()
	if w == nil {
		return false
	}
	if zp := w.zoomedPane(); zp != nil {
		return zp.vt.Dirty()
	}
	for _, p := range w.panes {
		if p.vt.Dirty() {
			return true
		}
	}
	return false
}

// statusHit is a clickable region of the status bar: [x0,x1] (inclusive)
// maps to an action. The tab-strip overflow arrows are select-window hits for
// the nearest hidden window on their side.
type statusHit struct {
	x0, x1 int
	action string // "select-window" (win in n) or "new-window"
	n      int
}

// statusHint is the right-aligned reminder for a new user: the prefix alone
// opens the key cheat-sheet, and ^b d leaves the session running.
const statusHint = "^b menu · ^b d detach "

// maxTabName caps a window name's width on its tab so one long name cannot
// push every other tab off the bar.
const maxTabName = 20

// Tab-strip overflow arrows, shown when windows are scrolled out of view on
// that side. Both are in CP437.
const (
	arrowLeft  = "«"
	arrowRight = "»"
)

// truncateWidth shortens s to at most max display columns, ending in "…"
// when it had to cut.
func truncateWidth(s string, max int) string {
	if vterm.StringWidth(s) <= max {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := vterm.RuneWidth(r)
		if w+rw > max-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteString("…")
	return b.String()
}

// visibleTabs picks the contiguous run of tabs [lo,hi] to show in room
// columns, given each tab's width. It always contains cur, then grows one tab
// at a time alternating right and left while the next one fits, so the
// current tab sits near the middle of what is shown. If cur alone is wider
// than room it is still the whole run (the frame edge clips it).
func visibleTabs(widths []int, cur, room int) (lo, hi int) {
	lo, hi = cur, cur
	used := widths[cur]
	for grew := true; grew; {
		grew = false
		if hi+1 < len(widths) && used+widths[hi+1] <= room {
			hi++
			used += widths[hi]
			grew = true
		}
		if lo > 0 && used+widths[lo-1] <= room {
			lo--
			used += widths[lo]
			grew = true
		}
	}
	return lo, hi
}

// buildStatus lays out the bar as
//
//	session  0 shell  1 vim  +  ZOOM          ^b menu · ^b d detach  15:04
//
// The session name and the active tab are solid colour pills, a hidden window
// with unseen output is in the alert colour, and the right side holds a hint
// and a clock, padded to the full width. When the tabs do not fit, the hint
// goes first; then the tab strip scrolls to keep the current window in view,
// with « / » marking windows hidden on either side (in the alert colour if
// one of them has unseen output). It also records clickable regions in
// s.statusHits. Caller holds mu.
func (s *session) buildStatus(cols int) []render.StatusSegment {
	s.statusHits = s.statusHits[:0]
	pal := &s.opts.palette

	var segs []render.StatusSegment
	col := 0 // running display column, for click regions and padding
	add := func(text string, st theme.Style) {
		if text == "" {
			return
		}
		segs = append(segs, render.StatusSegment{Text: text, FG: st.FG, BG: st.BG, Attr: st.Attr})
		col += vterm.StringWidth(text)
	}
	plain := func(text string, attr uint16) {
		add(text, theme.Style{FG: theme.Inherit, BG: theme.Inherit, Attr: attr})
	}

	add(" "+truncateWidth(s.name, maxTabName)+" ", pal.Session)
	plain(" ", 0)

	// Settle each window's activity mark first: the tabs and the overflow
	// arrows both read it, and session.dirty compares against it.
	tabs := s.tabScratch[:0]
	widths := s.tabWidths[:0]
	total := 0
	for i, w := range s.windows {
		if i == s.cur {
			// Being looked at: whatever it printed has been seen.
			w.clearOutput()
			w.activityShown = false
		} else {
			w.activityShown = w.hasOutput()
		}
		entry := fmt.Sprintf(" %d %s ", i, truncateWidth(w.name, maxTabName))
		tabs = append(tabs, entry)
		widths = append(widths, vterm.StringWidth(entry))
		total += widths[i]
	}
	s.tabScratch, s.tabWidths = tabs, widths

	var modes []string
	modesW := 0
	if w := s.current(); w != nil {
		if w.zoom != 0 {
			modes = append(modes, " ZOOM ")
			modesW += 1 + 6
		}
		if p := w.panes[w.active]; p != nil && p.copy != nil {
			modes = append(modes, " COPY ")
			modesW += 1 + 6
		}
	}

	const plusText = " + "
	clock := " " + time.Now().Format("15:04") + " "
	clockW := vterm.StringWidth(clock)
	// Columns left for the tab strip once everything else (and at least one
	// column of padding before the clock) has its place.
	room := cols - col - len(plusText) - modesW - 1 - clockW

	lo, hi := 0, len(tabs)-1
	overflow := total > room && len(tabs) > 0
	showClock := true
	if overflow {
		room -= 2 // one column for each arrow
		if s.cur >= 0 && s.cur < len(widths) && widths[s.cur] > room {
			// Even the current tab alone does not fit: give it the clock's room.
			showClock = false
			room += 1 + clockW
		}
		lo, hi = visibleTabs(widths, s.cur, room)
	}

	// arrow draws one overflow arrow selecting window target, alerting when
	// any window in [from,to) has unseen output.
	arrow := func(text string, target, from, to int) {
		st := theme.Style{FG: theme.Inherit, BG: theme.Inherit}
		for i := from; i < to; i++ {
			if s.windows[i].activityShown {
				st = pal.Alert
				break
			}
		}
		s.statusHits = append(s.statusHits, statusHit{x0: col, x1: col, action: "select-window", n: target})
		add(text, st)
	}
	if overflow {
		if lo > 0 {
			arrow(arrowLeft, lo-1, 0, lo)
		} else {
			plain(" ", 0)
		}
	}
	for i := lo; i <= hi; i++ {
		x0 := col
		switch w := s.windows[i]; {
		case i == s.cur:
			add(tabs[i], pal.Active)
		case w.activityShown:
			add(tabs[i], pal.Alert)
		default:
			add(tabs[i], pal.Tab)
		}
		s.statusHits = append(s.statusHits, statusHit{
			x0: x0, x1: col - 1, action: "select-window", n: i,
		})
	}
	if overflow {
		if hi < len(tabs)-1 {
			arrow(arrowRight, hi+1, hi+1, len(tabs))
		} else {
			plain(" ", 0)
		}
	}

	plusX0 := col
	plain(plusText, vterm.AttrBold)
	s.statusHits = append(s.statusHits, statusHit{x0: plusX0, x1: col - 1, action: "new-window"})

	for _, m := range modes {
		plain(" ", 0)
		add(m, pal.Mode)
	}

	if !showClock {
		if pad := cols - col; pad > 0 {
			plain(strings.Repeat(" ", pad), 0)
		}
		return segs
	}
	// Prefer "<hint> <clock>"; if that will not fit, drop the hint; if even the
	// clock will not fit, leave the row to be clipped at the frame edge.
	hint := statusHint
	if overflow || col+1+vterm.StringWidth(hint+clock) > cols {
		hint = ""
	}
	if pad := cols - col - vterm.StringWidth(hint+clock); pad >= 1 {
		plain(strings.Repeat(" ", pad), 0)
		plain(hint, 0)
		plain(clock, vterm.AttrBold)
	}
	return segs
}

// statusClick runs the action for a click at column x on the status row, if any
// clickable region covers it. Caller holds mu. It returns true if it acted.
func (s *session) statusClick(x int) bool {
	for _, h := range s.statusHits {
		if x < h.x0 || x > h.x1 {
			continue
		}
		switch h.action {
		case "select-window":
			if h.n >= 0 && h.n < len(s.windows) {
				s.cur = h.n
			}
		case "new-window":
			if _, err := s.spawnWindow(s.cols, s.contentRows()); err != nil {
				return false
			}
		}
		return true
	}
	return false
}

func (s *session) info(attached bool) protocol.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	panes := 0
	for _, w := range s.windows {
		panes += len(w.panes)
	}
	return protocol.SessionInfo{
		Name:     s.name,
		Windows:  len(s.windows),
		Panes:    panes,
		Attached: attached,
	}
}

func (s *session) shutdown() {
	s.deadOnce.Do(func() { close(s.dead) })
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.windows {
		w.closeAll()
	}
	s.windows = nil
}
