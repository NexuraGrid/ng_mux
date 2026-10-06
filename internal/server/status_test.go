package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/config"
	"github.com/MauricioJC3/ng_mux/internal/render"
	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

func statusText(segs []render.StatusSegment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestBuildStatusLayoutAndHints(t *testing.T) {
	_, _, sess := setupSession(t)
	sess.mu.Lock()
	segs := sess.buildStatus(80)
	hits := append([]statusHit(nil), sess.statusHits...)
	sess.mu.Unlock()

	text := statusText(segs)

	if !strings.HasPrefix(text, " 0   0 fakesh  + ") {
		t.Fatalf("status left side = %q, want session pill, tab, then +", text)
	}
	// The menu/detach reminder and a clock on the right.
	if !strings.Contains(text, "menu") || !strings.Contains(text, "detach") {
		t.Errorf("status bar is missing the menu/detach hint: %q", text)
	}
	if len(text) < 80 || strings.Count(text, ":") < 1 { // clock
		t.Errorf("status bar not padded to width or missing clock: %q", text)
	}

	// The [+] hit region must cover the actual "[+]" runes in the text.
	var plus *statusHit
	for i := range hits {
		if hits[i].action == "new-window" {
			plus = &hits[i]
		}
	}
	if plus == nil {
		t.Fatal("no new-window hit region recorded")
	}
	if got := text[plus.x0 : plus.x1+1]; got != " + " {
		t.Errorf("new-window hit covers %q at [%d,%d], want \" + \"", got, plus.x0, plus.x1)
	}
}

// tabSegment returns the status segment whose trimmed text is want.
func tabSegment(segs []render.StatusSegment, want string) (render.StatusSegment, bool) {
	for _, s := range segs {
		if strings.TrimSpace(s.Text) == want {
			return s, true
		}
	}
	return render.StatusSegment{}, false
}

func TestBuildStatusActiveWindowIsAPill(t *testing.T) {
	srv, _, sess := setupSession(t)
	exec(t, srv, "new-window") // two windows, second is current

	sess.mu.Lock()
	segs := sess.buildStatus(80)
	sess.mu.Unlock()

	active, ok := tabSegment(segs, "1 fakesh")
	if !ok || active.BG != theme.Dark().Active.BG || active.Attr&vterm.AttrBold == 0 {
		t.Errorf("active tab = %+v, want a bold pill on colour %d", active, theme.Dark().Active.BG)
	}
	inactive, ok := tabSegment(segs, "0 fakesh")
	if !ok || inactive.BG != render.InheritColour || inactive.Attr&vterm.AttrBold != 0 {
		t.Errorf("inactive tab = %+v, want plain bar colours", inactive)
	}
}

// A hidden window that printed something is flagged until it is shown.
func TestBuildStatusFlagsHiddenWindowActivity(t *testing.T) {
	srv, _, sess := setupSession(t)
	exec(t, srv, "new-window") // window 1 current, window 0 hidden

	sess.mu.Lock()
	sess.buildStatus(80) // settle: nothing flagged yet
	for _, p := range sess.windows[0].panes {
		p.output.Store(true)
	}
	if !sess.windows[0].hasOutput() || sess.windows[0].activityShown {
		sess.mu.Unlock()
		t.Fatal("setup: window 0 should have unshown output")
	}
	sess.mu.Unlock()

	if !sess.dirty() {
		t.Error("new output in a hidden window should request a repaint")
	}

	sess.mu.Lock()
	segs := sess.buildStatus(80)
	sess.mu.Unlock()
	tab, _ := tabSegment(segs, "0 fakesh")
	if tab.FG != theme.Dark().Alert.FG {
		t.Errorf("hidden window with output = %+v, want FG %d", tab, theme.Dark().Alert.FG)
	}

	// Showing the window clears the flag.
	exec(t, srv, "select-window 0")
	sess.mu.Lock()
	sess.buildStatus(80)
	shown := sess.windows[0].hasOutput()
	sess.mu.Unlock()
	if shown {
		t.Error("viewing a window should clear its activity flag")
	}
}

func TestBuildStatusNarrowTerminalDropsHint(t *testing.T) {
	_, _, sess := setupSession(t)
	sess.mu.Lock()
	segs := sess.buildStatus(30)
	sess.mu.Unlock()

	text := statusText(segs)
	if strings.Contains(text, "detach") {
		t.Errorf("hint should be dropped when it will not fit: %q", text)
	}
}

// A window name with a double-width character must be measured in columns, not
// runes: the [+] hit region, the padding and the clock all key off that width.
func TestBuildStatusAccountsForWideWindowName(t *testing.T) {
	srv, _, sess := setupSession(t)
	exec(t, srv, "rename-window 名前") // 4 display columns, 2 runes

	sess.mu.Lock()
	segs := sess.buildStatus(80)
	hits := append([]statusHit(nil), sess.statusHits...)
	name := sess.windows[sess.cur].name
	sess.mu.Unlock()

	if name != "名前" {
		t.Fatalf("window name = %q, want 名前", name)
	}

	var plusX0 int
	for _, h := range hits {
		if h.action == "new-window" {
			plusX0 = h.x0
		}
	}
	// " 0 " (3) + " " (1) + " 0 " (3) + name (4 cols) + " " (1) = 12.
	if plusX0 != 12 {
		t.Fatalf("+ hit x0 = %d, want 12 (wide name counted as 4 columns)", plusX0)
	}

	// The bar is still exactly one screen wide in display columns.
	if w := vterm.StringWidth(statusText(segs)); w != 80 {
		t.Fatalf("status bar width = %d columns, want 80", w)
	}
}

func TestTruncateWidth(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 20, "short"},
		{"abcdefghij", 5, "abcd…"},
		{"名前名前", 5, "名前…"}, // never splits a wide rune
	}
	for _, c := range cases {
		if got := truncateWidth(c.in, c.max); got != c.want {
			t.Errorf("truncateWidth(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

// statusAt returns the text of the status bar between display columns x0 and
// x1 inclusive. The bar under test is single-width throughout, so columns are
// runes.
func statusAt(text string, x0, x1 int) string {
	r := []rune(text)
	if x0 < 0 || x1 >= len(r) || x0 > x1 {
		return ""
	}
	return string(r[x0 : x1+1])
}

// With more windows than fit, the tab strip scrolls to keep the current one in
// view, « / » mark the hidden ones, and every click region matches what is
// drawn.
func TestBuildStatusOverflowScrollsToCurrentWindow(t *testing.T) {
	srv, _, sess := setupSession(t)
	for i := 0; i < 14; i++ {
		exec(t, srv, "new-window")
	}
	exec(t, srv, "select-window 7")

	const cols = 50
	sess.mu.Lock()
	segs := sess.buildStatus(cols)
	hits := append([]statusHit(nil), sess.statusHits...)
	sess.mu.Unlock()
	text := statusText(segs)

	if w := vterm.StringWidth(text); w != cols {
		t.Fatalf("status bar width = %d columns, want %d: %q", w, cols, text)
	}
	if !strings.Contains(text, " 7 fakesh ") {
		t.Fatalf("current window 7 is not visible: %q", text)
	}
	if strings.Contains(text, "detach") {
		t.Errorf("the hint should be dropped before tabs scroll: %q", text)
	}

	var left, right *statusHit
	shown := map[int]bool{}
	for i := range hits {
		h := &hits[i]
		got := statusAt(text, h.x0, h.x1)
		switch {
		case got == arrowLeft:
			left = h
		case got == arrowRight:
			right = h
		case h.action == "select-window":
			if want := fmt.Sprintf(" %d fakesh ", h.n); got != want {
				t.Errorf("tab hit for window %d covers %q, want %q", h.n, got, want)
			}
			shown[h.n] = true
		case h.action == "new-window":
			if got != " + " {
				t.Errorf("new-window hit covers %q, want \" + \"", got)
			}
		}
	}
	if left == nil || right == nil {
		t.Fatalf("want both overflow arrows, got left=%v right=%v in %q", left, right, text)
	}
	lo, hi := 15, -1
	for n := range shown {
		lo, hi = min(lo, n), max(hi, n)
	}
	if hi-lo+1 != len(shown) || !shown[7] {
		t.Fatalf("visible tabs %v should be one contiguous run around 7", shown)
	}
	if left.n != lo-1 || right.n != hi+1 {
		t.Errorf("arrows select %d / %d, want the nearest hidden windows %d / %d", left.n, right.n, lo-1, hi+1)
	}

	// Clicking an arrow selects that hidden window.
	sess.mu.Lock()
	acted := sess.statusClick(right.x0)
	cur := sess.cur
	sess.mu.Unlock()
	if !acted || cur != hi+1 {
		t.Fatalf("clicking » selected window %d (acted %v), want %d", cur, acted, hi+1)
	}
}

// At the ends of the window list only the arrow pointing at hidden windows is
// drawn; the other side keeps its column blank so the strip does not shift.
func TestBuildStatusOverflowAtFirstWindow(t *testing.T) {
	srv, _, sess := setupSession(t)
	for i := 0; i < 14; i++ {
		exec(t, srv, "new-window")
	}
	exec(t, srv, "select-window 0")

	sess.mu.Lock()
	segs := sess.buildStatus(50)
	sess.mu.Unlock()
	text := statusText(segs)
	if strings.Contains(text, arrowLeft) || !strings.Contains(text, arrowRight) {
		t.Fatalf("at window 0 want only », got %q", text)
	}
	if !strings.Contains(text, " 0 fakesh ") {
		t.Fatalf("window 0 not visible: %q", text)
	}
}

// An arrow over a hidden window with unseen output takes the alert colour.
func TestBuildStatusOverflowArrowAlerts(t *testing.T) {
	srv, _, sess := setupSession(t)
	for i := 0; i < 14; i++ {
		exec(t, srv, "new-window") // window 14 is current
	}
	sess.mu.Lock()
	for _, p := range sess.windows[0].panes {
		p.output.Store(true)
	}
	segs := sess.buildStatus(50)
	sess.mu.Unlock()

	arrow, ok := tabSegment(segs, arrowLeft)
	if !ok {
		t.Fatalf("no « in %q", statusText(segs))
	}
	if alert := theme.Dark().Alert; arrow.FG != alert.FG || arrow.Attr != alert.Attr {
		t.Errorf("« over a window with output = %+v, want the alert style %+v", arrow, alert)
	}
}

func TestVisibleTabsKeepsCurrentCentred(t *testing.T) {
	widths := []int{5, 5, 5, 5, 5, 5, 5, 5, 5}
	lo, hi := visibleTabs(widths, 4, 15)
	if lo != 3 || hi != 5 {
		t.Errorf("visibleTabs(cur 4, room 15) = [%d,%d], want [3,5]", lo, hi)
	}
	lo, hi = visibleTabs(widths, 8, 15)
	if lo != 6 || hi != 8 {
		t.Errorf("at the last tab = [%d,%d], want [6,8]", lo, hi)
	}
	lo, hi = visibleTabs(widths, 2, 3) // too narrow for even the current tab
	if lo != 2 || hi != 2 {
		t.Errorf("narrower than one tab = [%d,%d], want [2,2]", lo, hi)
	}
}

// The mono theme draws pills without colour: normal video inside the
// reverse-video bar.
func TestBuildStatusMonoThemeHasNoColour(t *testing.T) {
	srv, _ := newTestServerWith(t, func(o *sessionOpts) { o.palette = theme.Mono() })
	sess, err := srv.getOrCreateSession("0")
	if err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	segs := sess.buildStatus(80)
	sess.mu.Unlock()
	for _, s := range segs {
		for _, c := range []int{s.FG, s.BG} {
			if c != theme.Inherit && c != theme.Default {
				t.Fatalf("mono segment %+v uses colour %d", s, c)
			}
		}
	}
	active, _ := tabSegment(segs, "0 fakesh")
	if active.FG != theme.Default || active.Attr&vterm.AttrBold == 0 {
		t.Errorf("mono active tab = %+v, want a bold default-colour pill", active)
	}
}

// Per-element colour keys reach the bar through config.Palette.
func TestBuildStatusUsesConfiguredTabColours(t *testing.T) {
	cfg := config.Default()
	cfg.Colours["tab-active-bg"] = 5
	cfg.Colours["tab-fg"] = 6
	srv, _ := newTestServerWith(t, func(o *sessionOpts) { o.palette = cfg.Palette() })
	sess, err := srv.getOrCreateSession("0")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, srv, "new-window") // window 1 current, window 0 a plain tab
	sess.mu.Lock()
	segs := sess.buildStatus(80)
	sess.mu.Unlock()
	active, ok := tabSegment(segs, "1 fakesh")
	if want := theme.Dark().Active; !ok || active.BG != 5 || active.FG != want.FG || active.Attr != want.Attr {
		t.Errorf("active tab = %+v, want the dark pill on colour 5", active)
	}
	inactive, ok := tabSegment(segs, "0 fakesh")
	if !ok || inactive.FG != 6 || inactive.BG != theme.Inherit {
		t.Errorf("inactive tab = %+v, want text colour 6 on the bar", inactive)
	}
}
