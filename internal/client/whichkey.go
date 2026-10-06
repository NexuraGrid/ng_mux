package client

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/MauricioJC3/ng_mux/internal/termio"
	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// whichKeyRow is one line of the prefix cheat-sheet: the key(s) pressed after
// the prefix and a short description of what they do.
type whichKeyRow struct {
	keys string
	desc string
}

// whichKeySection is a titled group of cheat-sheet rows. A section is drawn as
// one block and is never split across the panel's columns.
type whichKeySection struct {
	title string
	rows  []whichKeyRow
}

// whichKeyPrefixKey stands for the prefix's own label in a row's keys; it is
// filled in by whichKeySections, so a custom prefix reads correctly.
const whichKeyPrefixKey = "{prefix}"

// builtinWhichKey mirrors defaultKeyCommands plus the keys forwardInput and
// resolveKey handle specially (digits, arrows, ':', ',', '$', 'm', the prefix
// itself) in reading order with human labels. resolveKey and forwardInput stay
// the single source of truth for behaviour; this table is display only, so a
// binding change must be reflected here by hand. Descriptions are kept short
// (25 columns at most) so two columns fit an 80-column terminal.
var builtinWhichKey = []whichKeySection{
	{"Panes", []whichKeyRow{
		{`"`, "split top / bottom"},
		{"%", "split left / right"},
		{"o / ;", "next / previous pane"},
		{"arrows", "↑ ← previous · ↓ → next"},
		{"H J K L", "resize pane"},
		{"z", "zoom pane (toggle)"},
		{"q", "show pane numbers"},
		{"{ / }", "swap with previous / next"},
		{"!", "break into own window"},
		{"x", "close pane"},
	}},
	{"Windows (tabs)", []whichKeyRow{
		{"c", "new window"},
		{",", "rename window"},
		{"n / p", "next / previous window"},
		{"0-9", "select by number"},
		{"&", "close window"},
	}},
	{"Sessions", []whichKeyRow{
		{"$", "rename session"},
		{"( / )", "previous / next session"},
		{"m", "session help"},
		{"d", "detach (keeps running)"},
	}},
	{"Copy & paste", []whichKeyRow{
		{"[", "copy mode / scrollback"},
		{"]", "paste"},
	}},
	{"Other", []whichKeyRow{
		{":", "command prompt"},
		{whichKeyPrefixKey, "send a literal prefix"},
	}},
}

// whichKeyConfigTitle heads the section listing the user's own bindings.
const whichKeyConfigTitle = "Your bindings"

// whichKeyMinCols / whichKeyMinRows are the smallest terminal the popup will
// draw itself on; below that it silently does nothing.
const (
	whichKeyMinCols = 24
	whichKeyMinRows = 8
)

// whichKey layout constants: the gap between side-by-side columns, the widest
// key and description columns (longer text is cut with "…").
const (
	whichKeyGap      = 3
	whichKeyMaxKeyW  = 10
	whichKeyMaxDescW = 25
)

// showWhichKey draws the prefix cheat-sheet as a modal panel centred above the
// status bar and returns a function that erases it again. It is called right
// after the prefix key is pressed, while the client blocks waiting for the next
// key, so the panel behaves like Neovim's which-key: press the prefix, see the
// choices, press a key. A concurrent server frame can repaint a pane behind the
// panel; that is the same trade-off the ':' prompt makes and it self-heals on
// the Refresh the caller sends after hiding.
func showWhichKey(out *lockedWriter, term *os.File, km keymap, pal *theme.Palette) func() {
	size, err := termio.GetSize(term)
	if err != nil || size.Cols < whichKeyMinCols || size.Rows < whichKeyMinRows {
		return func() {}
	}

	// Everything above the status bar (the last row) is available.
	avail := size.Rows - 1
	pop, ok := whichKeyPopup(prefixLabel(km.prefix), whichKeySections(km), size.Cols-2, avail)
	if !ok {
		return func() {}
	}
	startRow := max((avail-pop.height())/2+1, 1)
	startCol := max((size.Cols-pop.width())/2+1, 1)

	var b strings.Builder
	b.WriteString("\x1b[?25l") // hide the cursor while the panel is up
	pop.paint(&b, startRow, startCol, pal)
	out.WriteString(b.String())

	return func() {
		var c strings.Builder
		pop.erase(&c, startRow, startCol)
		c.WriteString("\x1b[?25h")
		out.WriteString(c.String())
	}
}

// whichKeySections is the full cheat-sheet for km: the built-in sections with
// the prefix label filled in, then the user's bindings when there are any.
func whichKeySections(km keymap) []whichKeySection {
	label := prefixLabel(km.prefix)
	out := make([]whichKeySection, 0, len(builtinWhichKey)+1)
	for _, s := range builtinWhichKey {
		rows := make([]whichKeyRow, len(s.rows))
		for i, r := range s.rows {
			rows[i] = whichKeyRow{keys: strings.ReplaceAll(r.keys, whichKeyPrefixKey, label), desc: r.desc}
		}
		out = append(out, whichKeySection{title: s.title, rows: rows})
	}
	if cfg := configWhichKey(km); len(cfg) > 0 {
		out = append(out, whichKeySection{title: whichKeyConfigTitle, rows: cfg})
	}
	return out
}

// configWhichKey lists the user's own `bind` directives so a custom config
// shows up in the cheat-sheet too.
func configWhichKey(km keymap) []whichKeyRow {
	if len(km.binds) == 0 {
		return nil
	}
	keys := make([]string, 0, len(km.binds))
	for k := range km.binds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]whichKeyRow, 0, len(keys))
	for _, k := range keys {
		line, ok := km.resolveKey(k[0])
		if !ok {
			line = km.binds[k]
		}
		out = append(out, whichKeyRow{keys: k, desc: line})
	}
	return out
}

// whichKeyBox renders the cheat-sheet as a bordered panel no wider than
// maxWidth columns and no taller than maxRows lines. Rows that do not fit are
// dropped from the end and replaced with a "+N more" note. It returns the
// panel as equal-width plain-text lines (no ANSI), or nil when it cannot fit.
func whichKeyBox(title string, sections []whichKeySection, maxWidth, maxRows int) []string {
	pop, ok := whichKeyPopup(title, sections, maxWidth, maxRows)
	if !ok {
		return nil
	}
	return pop.lines()
}

// whichKeyPopup is whichKeyBox's layout as a popup, ready to paint. Sections
// go in two columns side by side when maxWidth allows it, one otherwise.
func whichKeyPopup(title string, sections []whichKeySection, maxWidth, maxRows int) (popup, bool) {
	if maxWidth < 12 || maxRows < 3 {
		return popup{}, false
	}

	keyW, descW, total := 0, 0, 0
	for _, s := range sections {
		for _, r := range s.rows {
			keyW = max(keyW, vterm.StringWidth(r.keys))
			descW = max(descW, vterm.StringWidth(r.desc))
			total++
		}
	}
	if total == 0 {
		return popup{}, false
	}
	keyW = min(keyW, whichKeyMaxKeyW)
	descW = min(descW, whichKeyMaxDescW)
	for _, s := range sections {
		// A heading spans key + "  " + desc and needs its title plus a little
		// rule.
		descW = max(descW, vterm.StringWidth(s.title)+2-keyW-2)
	}

	ncols := 1
	if 4+2*(keyW+2+descW)+whichKeyGap <= maxWidth {
		ncols = 2
	} else if 4+keyW+2+descW > maxWidth {
		descW = maxWidth - 4 - keyW - 2
	}
	if descW < 4 {
		return popup{}, false
	}

	bodyCap := maxRows - 2 // top + bottom border
	if bodyCap < 1 {
		return popup{}, false
	}

	// Drop rows from the end until the layout fits, keeping a line for the
	// "+N more" note once anything is dropped.
	secs := sections
	hidden := 0
	for {
		capRows := bodyCap
		if hidden > 0 {
			capRows--
		}
		cols := layoutWhichKey(secs, ncols)
		if tallest(cols) <= capRows {
			pop := popup{title: title, footer: "press a key · Esc to cancel", gap: whichKeyGap}
			for _, c := range cols {
				pop.cols = append(pop.cols, popupCol{rows: c, keyW: keyW, textW: descW})
			}
			if hidden > 0 {
				pop.note = fmt.Sprintf("+%d more", hidden)
			}
			return pop, true
		}
		if capRows < 2 {
			return popup{}, false
		}
		secs = dropLastRow(secs)
		hidden++
		if len(secs) == 0 {
			return popup{}, false
		}
	}
}

// dropLastRow returns sections without their very last row, and without the
// last section when that leaves it empty. It never modifies its argument.
func dropLastRow(secs []whichKeySection) []whichKeySection {
	out := append([]whichKeySection(nil), secs...)
	last := out[len(out)-1]
	last.rows = last.rows[:len(last.rows)-1]
	if len(last.rows) == 0 {
		return out[:len(out)-1]
	}
	out[len(out)-1] = last
	return out
}

// layoutWhichKey stacks sections into ncols (1 or 2) columns of popup rows: a
// heading, its entries, and a blank spacer between sections. With two
// columns, the sections keep their order (left column first) and the split
// point is the one that makes the taller column as short as possible.
func layoutWhichKey(secs []whichKeySection, ncols int) [][]popupRow {
	stack := func(ss []whichKeySection) []popupRow {
		var rows []popupRow
		for i, s := range ss {
			if i > 0 {
				rows = append(rows, popupRow{kind: rowSpan})
			}
			rows = append(rows, popupRow{text: s.title, kind: rowHeader})
			for _, r := range s.rows {
				rows = append(rows, popupRow{key: r.keys, text: r.desc})
			}
		}
		return rows
	}
	if ncols < 2 || len(secs) < 2 {
		return [][]popupRow{stack(secs)}
	}
	var best [][]popupRow
	for k := 1; k < len(secs); k++ {
		cols := [][]popupRow{stack(secs[:k]), stack(secs[k:])}
		if best == nil || tallest(cols) < tallest(best) {
			best = cols
		}
	}
	return best
}

func tallest(cols [][]popupRow) int {
	n := 0
	for _, c := range cols {
		n = max(n, len(c))
	}
	return n
}

// prefixLabel renders a prefix byte the way a user would say it: a control byte
// as "Ctrl-x", a printable byte as itself.
func prefixLabel(b byte) string {
	if b >= 1 && b <= 26 {
		return "Ctrl-" + string(rune('a'+b-1))
	}
	if b >= 0x20 && b < 0x7f {
		return string(rune(b))
	}
	return fmt.Sprintf("0x%02x", b)
}
