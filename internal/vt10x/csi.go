package vt10x

import (
	"fmt"
	"strconv"
	"strings"
)

// Device-attribute responses answered by handleCSI's 'c' case. Reported "T" =
// tmux, matching what tmux itself reports for its secondary DA.
var (
	primaryDAResponse   = []byte("\x1b[?1;2c")
	secondaryDAResponse = []byte("\x1b[>84;0;0c")
)

// CSI (Control Sequence Introducer)
// ESC+[
type csiEscape struct {
	buf  []byte
	args []int
	mode byte
	priv bool
	gt   bool // leading '>' marker, e.g. secondary DA: "ESC[>c", "ESC[>0c"
	// marked is set for any leading private marker ('?', '>', '<', '='). A
	// marked final byte is a different command from the unmarked one:
	// "ESC[>4;2m" is XTMODKEYS, not SGR, and "ESC[>1u" is a kitty keyboard
	// push, not DECRC.
	marked bool
	// inter is the intermediate byte (0x20-0x2F) just before the final byte,
	// or 0. "ESC[5 q" (DECSCUSR) is a different command from "ESC[5q".
	inter byte
}

func (c *csiEscape) reset() {
	c.buf = c.buf[:0]
	c.args = c.args[:0]
	c.mode = 0
	c.priv = false
	c.gt = false
	c.marked = false
	c.inter = 0
}

func (c *csiEscape) put(b byte) bool {
	c.buf = append(c.buf, b)
	if b >= 0x40 && b <= 0x7E || len(c.buf) >= 256 {
		c.parse()
		return true
	}
	return false
}

func (c *csiEscape) parse() {
	c.mode = c.buf[len(c.buf)-1]
	if len(c.buf) == 1 {
		return
	}
	s := string(c.buf)
	c.args = c.args[:0]
	switch s[0] {
	case '?':
		c.priv = true
		s = s[1:]
	case '>':
		// Secondary-DA / private '>' marker (e.g. "ESC[>c"). Stripped the same
		// way as '?' so the remaining digits parse normally instead of
		// tripping strconv.Atoi on a leading '>' and silently truncating args.
		c.gt = true
		s = s[1:]
	case '<', '=':
		s = s[1:]
	}
	c.marked = len(s) < len(c.buf) // a marker byte was stripped
	s = s[:len(s)-1]
	if n := len(s); n > 0 && s[n-1] >= 0x20 && s[n-1] <= 0x2F {
		c.inter = s[n-1]
		s = s[:n-1]
	}
	ss := strings.Split(s, ";")
	for _, p := range ss {
		// An empty parameter ("ESC[;5H", fzf's "ESC[;1;38;5;161m") stands
		// for the command's default (ECMA-48). Stopping at it, as Atoi("")
		// failing used to, dropped every parameter after it.
		if p == "" {
			c.args = append(c.args, argDefault)
			continue
		}
		if strings.Contains(p, ":") {
			args, ok := colonArgs(p)
			if !ok {
				break
			}
			c.args = append(c.args, args...)
			continue
		}
		i, err := strconv.Atoi(p)
		if err != nil {
			//t.logf("invalid CSI arg '%s'\n", p)
			break
		}
		// A hostile or malformed sequence (e.g. "\x1b[-5@") can carry a
		// negative or absurdly large parameter. strconv.Atoi happily parses
		// both, but nothing downstream expects it: handlers use these values
		// as slice indices and loop bounds (insertBlanks, deleteChars, CHT,
		// CBT, ...) and a negative or huge value there is undefined behavior
		// at best and an out-of-range panic at worst. Clamp at the source so
		// no negative value ever reaches a handler; handlers additionally
		// clamp their own inputs as defense in depth.
		c.args = append(c.args, clampArg(i))
	}
}

// colonArgs expands an SGR parameter with ':' sub-parameters (ITU T.416) into
// the equivalent ';' parameters, so "38:2::r:g:b" sets the same color as
// "38;2;r;g;b" instead of stopping the parse. Handled: 38/48 with :2:[cs:]r:g:b
// or :5:n, and the underline style 4:n (4:0 is off). A malformed 38/48 is
// dropped; any other parameter keeps only its main value. ok is false when
// the main value is not a number.
func colonArgs(p string) (args []int, ok bool) {
	sub := strings.Split(p, ":")
	n := make([]int, len(sub))
	for i, s := range sub {
		v, err := strconv.Atoi(s)
		if err != nil {
			if i == 0 {
				return nil, false
			}
			v = 0 // an empty sub-parameter, e.g. the color space in 38:2::r:g:b
		}
		n[i] = clampArg(v)
	}
	switch n[0] {
	case 38, 48:
		switch {
		case len(n) == 3 && n[1] == 5:
			return []int{n[0], 5, n[2]}, true
		case len(n) == 5 && n[1] == 2:
			return []int{n[0], 2, n[2], n[3], n[4]}, true
		case len(n) == 6 && n[1] == 2:
			return []int{n[0], 2, n[3], n[4], n[5]}, true
		}
		return nil, true
	case 4:
		if len(n) > 1 && n[1] == 0 {
			return []int{24}, true
		}
		return []int{4}, true
	}
	return n[:1], true
}

// maxCSIArg bounds a single CSI parameter. 65535 comfortably covers every
// legitimate use (repeat counts, cursor moves, mode numbers) while keeping a
// clamped value far too small to threaten any loop bound or slice index
// derived from it, even before a handler applies its own bound.
const maxCSIArg = 65535

func clampArg(i int) int {
	if i < 0 {
		return 0
	}
	if i > maxCSIArg {
		return maxCSIArg
	}
	return i
}

// argDefault marks an empty parameter. clampArg never yields a negative value,
// so it cannot collide with a real one.
const argDefault = -1

func (c *csiEscape) arg(i, def int) int {
	if i >= len(c.args) || i < 0 || c.args[i] == argDefault {
		return def
	}
	return c.args[i]
}

// argsOr returns the parameter list with every empty parameter replaced by
// def, for handlers (SGR, SM/RM) that walk the whole list.
func (c *csiEscape) argsOr(def int) []int {
	for i, a := range c.args {
		if a == argDefault {
			c.args[i] = def
		}
	}
	return c.args
}

// maxarg takes the maximum of arg(i, def) and def
func (c *csiEscape) maxarg(i, def int) int {
	return max(c.arg(i, def), def)
}

func (t *State) handleCSI() {
	c := &t.csi
	if c.inter != 0 {
		// An intermediate byte makes a different command from the plain
		// final byte ("ESC[5 @" is SL, not ICH). DECSCUSR is the only one
		// handled; the rest are ignored rather than run as the wrong thing.
		if c.inter == ' ' && c.mode == 'q' && !c.marked {
			t.cursorStyle = min(c.arg(0, 0), 6)
			return
		}
		if c.inter == '$' && c.mode == 'p' && c.priv {
			t.reportPrivateMode(c.arg(0, 0))
			return
		}
		goto unknown
	}
	switch c.mode {
	default:
		goto unknown
	case '@': // ICH - insert <n> blank char
		t.insertBlanks(c.arg(0, 1))
	case 'A': // CUU - cursor <n> up
		t.moveTo(t.cur.X, t.cur.Y-c.maxarg(0, 1))
	case 'B', 'e': // CUD, VPR - cursor <n> down
		t.moveTo(t.cur.X, t.cur.Y+c.maxarg(0, 1))
	case 'c': // DA - device attributes
		// A shell such as fish sends a Primary DA request on startup and
		// blocks for ~10s if nothing replies, then runs degraded. Answering
		// here (rather than by scanning raw pty bytes upstream in vterm) means
		// the query is recognized correctly even when it arrives split across
		// two separate Write calls: t.state carries the parser across calls,
		// so "ESC[" in one Write and "c" in the next still reach this case.
		// The responses mirror what tmux reports; Ps is otherwise ignored, as
		// real terminals do.
		if c.gt {
			t.w.Write(secondaryDAResponse)
		} else {
			t.w.Write(primaryDAResponse)
		}
	case 'C', 'a': // CUF, HPR - cursor <n> forward
		t.moveTo(t.cur.X+c.maxarg(0, 1), t.cur.Y)
	case 'D': // CUB - cursor <n> backward
		t.moveTo(t.cur.X-c.maxarg(0, 1), t.cur.Y)
	case 'E': // CNL - cursor <n> down and first col
		t.moveTo(0, t.cur.Y+c.arg(0, 1))
	case 'F': // CPL - cursor <n> up and first col
		t.moveTo(0, t.cur.Y-c.arg(0, 1))
	case 'g': // TBC - tabulation clear
		switch c.arg(0, 0) {
		// clear current tab stop
		case 0:
			t.tabs[t.cur.X] = false
		// clear all tabs
		case 3:
			for i := range t.tabs {
				t.tabs[i] = false
			}
		default:
			goto unknown
		}
	case 'G', '`': // CHA, HPA - Move to <col>
		t.moveTo(c.arg(0, 1)-1, t.cur.Y)
	case 'H', 'f': // CUP, HVP - move to <row> <col>
		t.moveAbsTo(c.arg(1, 1)-1, c.arg(0, 1)-1)
	case 'I': // CHT - cursor forward tabulation <n> tab stops
		n := boundedRepeat(c.arg(0, 1), t.cols)
		for i := 0; i < n; i++ {
			t.putTab(true)
		}
	case 'J': // ED - clear screen
		// TODO: sel.ob.x = -1
		switch c.arg(0, 0) {
		case 0: // below
			t.clear(t.cur.X, t.cur.Y, t.cols-1, t.cur.Y)
			if t.cur.Y < t.rows-1 {
				t.clear(0, t.cur.Y+1, t.cols-1, t.rows-1)
			}
		case 1: // above
			if t.cur.Y > 1 {
				t.clear(0, 0, t.cols-1, t.cur.Y-1)
			}
			t.clear(0, t.cur.Y, t.cur.X, t.cur.Y)
		case 2: // all
			t.clear(0, 0, t.cols-1, t.rows-1)
		default:
			goto unknown
		}
	case 'K': // EL - clear line
		switch c.arg(0, 0) {
		case 0: // right
			t.clear(t.cur.X, t.cur.Y, t.cols-1, t.cur.Y)
		case 1: // left
			t.clear(0, t.cur.Y, t.cur.X, t.cur.Y)
		case 2: // all
			t.clear(0, t.cur.Y, t.cols-1, t.cur.Y)
		}
	case 'S': // SU - scroll <n> lines up
		t.scrollUpHistory(c.arg(0, 1))
	case 'T': // SD - scroll <n> lines down
		t.scrollDown(t.top, c.arg(0, 1))
	case 'L': // IL - insert <n> blank lines
		t.insertBlankLines(c.arg(0, 1))
	case 'l': // RM - reset mode
		t.setMode(c.priv, false, c.argsOr(0))
	case 'M': // DL - delete <n> lines
		t.deleteLines(c.arg(0, 1))
	case 'X': // ECH - erase <n> chars
		n := c.arg(0, 1)
		if n < 1 {
			n = 1
		}
		t.clear(t.cur.X, t.cur.Y, t.cur.X+n-1, t.cur.Y)
	case 'P': // DCH - delete <n> chars
		t.deleteChars(c.arg(0, 1))
	case 'Z': // CBT - cursor backward tabulation <n> tab stops
		n := boundedRepeat(c.arg(0, 1), t.cols)
		for i := 0; i < n; i++ {
			t.putTab(false)
		}
	case 'd': // VPA - move to <row>
		t.moveAbsTo(t.cur.X, c.arg(0, 1)-1)
	case 'h': // SM - set terminal mode
		t.setMode(c.priv, true, c.argsOr(0))
	case 'm': // SGR - terminal attribute (color)
		if c.marked {
			goto unknown // XTMODKEYS and friends; never attributes
		}
		t.setAttr(c.argsOr(0))
	case 'n':
		switch c.arg(0, 0) {
		case 5: // DSR - device status report
			t.w.Write([]byte("\033[0n"))
		case 6: // CPR - cursor position report
			t.w.Write([]byte(fmt.Sprintf("\033[%d;%dR", t.cur.Y+1, t.cur.X+1)))
		}
	case 'b': // REP - repeat the last printed character <n> times
		if c.marked || t.lastChar == 0 {
			goto unknown
		}
		// Bounded by the screen: more repeats than cells only rewrite them.
		for n := min(c.arg(0, 1), t.cols*t.rows); n > 0; n-- {
			t.parse(t.lastChar)
		}
	case 'r': // DECSTBM - set scrolling region
		if c.priv {
			goto unknown
		} else {
			t.setScroll(c.arg(0, 1)-1, c.arg(1, t.rows)-1)
			t.moveAbsTo(0, 0)
		}
	case 's': // DECSC - save cursor position (ANSI.SYS)
		if c.marked {
			goto unknown
		}
		t.saveCursor()
	case 'u': // DECRC - restore cursor position (ANSI.SYS)
		if c.marked {
			goto unknown // kitty keyboard protocol push/pop/query/set
		}
		t.restoreCursor()
	}
	return
unknown: // TODO: get rid of this goto
	t.logf("unknown CSI sequence '%c'\n", c.mode)
	// TODO: c.dump()
}

// reportPrivateMode answers DECRQM (CSI ? Ps $ p) with CSI ? Ps ; Pm $ y:
// 1 set, 2 reset, 0 not recognized. Only the modes apps commonly probe for
// before using them are reported; the rest answer "not recognized".
func (t *State) reportPrivateMode(mode int) {
	var flag ModeFlag
	switch mode {
	case 2004:
		flag = ModeBracketedPaste
	case 2026:
		flag = ModeSyncOutput
	}
	state := 0
	if flag != 0 {
		state = 2
		if t.mode&flag != 0 {
			state = 1
		}
	}
	t.w.Write([]byte(fmt.Sprintf("\x1b[?%d;%d$y", mode, state)))
}

// boundedRepeat caps a CSI repeat-count parameter at cols, so a huge parameter
// (post-clampArg, up to 65535) cannot spin a tab-stop loop far longer than the
// line it operates on could ever need. n == 0 is left as-is (no repeat), the
// existing behavior for an explicit zero count.
func boundedRepeat(n, cols int) int {
	if cols > 0 && n > cols {
		n = cols
	}
	return n
}
