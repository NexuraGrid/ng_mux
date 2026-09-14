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
}

func (c *csiEscape) reset() {
	c.buf = c.buf[:0]
	c.args = c.args[:0]
	c.mode = 0
	c.priv = false
	c.gt = false
	c.marked = false
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
	ss := strings.Split(s, ";")
	for _, p := range ss {
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

func (c *csiEscape) arg(i, def int) int {
	if i >= len(c.args) || i < 0 {
		return def
	}
	return c.args[i]
}

// maxarg takes the maximum of arg(i, def) and def
func (c *csiEscape) maxarg(i, def int) int {
	return max(c.arg(i, def), def)
}

func (t *State) handleCSI() {
	c := &t.csi
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
		t.setMode(c.priv, false, c.args)
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
		t.setMode(c.priv, true, c.args)
	case 'm': // SGR - terminal attribute (color)
		if c.marked {
			goto unknown // XTMODKEYS and friends; never attributes
		}
		t.setAttr(c.args)
	case 'n':
		switch c.arg(0, 0) {
		case 5: // DSR - device status report
			t.w.Write([]byte("\033[0n"))
		case 6: // CPR - cursor position report
			t.w.Write([]byte(fmt.Sprintf("\033[%d;%dR", t.cur.Y+1, t.cur.X+1)))
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
