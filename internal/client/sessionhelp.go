package client

import (
	"os"
	"strings"

	"github.com/MauricioJC3/ng_mux/internal/termio"
	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// sessionHelpRow is one line of the Ctrl-b m panel: a short label and the exact
// command that does it. NAME is a placeholder the reader fills in.
type sessionHelpRow struct{ label, cmd string }

// sessionHelp is the cheat-sheet shown by Ctrl-b m. It answers the questions a
// new user hits first: how do I start another session, how do I get back into
// one, how do I see what is still running.
var sessionHelp = []sessionHelpRow{
	{"new", "ngmux new -s NAME"},
	{"new (attached)", "Ctrl-b : new-session -s NAME"},
	{"attach", "ngmux attach -t NAME"},
	{"list", "ngmux ls"},
	{"switch", "Ctrl-b (   Ctrl-b )"},
	{"rename", "Ctrl-b $   (asks for a name)"},
	{"detach", "Ctrl-b d   (keeps it running)"},
	{"kill", "ngmux kill-session -t NAME"},
}

const sessionHelpTitle = "sessions"

// showSessionHelp draws the session cheat-sheet centred on the terminal and
// returns a function that erases it. Like showWhichKey it is a transient
// overlay: a concurrent server frame may repaint behind it, and the caller
// sends a Refresh once it is dismissed.
func showSessionHelp(out *lockedWriter, term *os.File, pal *theme.Palette) func() {
	size, err := termio.GetSize(term)
	if err != nil || size.Cols < whichKeyMinCols || size.Rows < whichKeyMinRows {
		return func() {}
	}

	pop, ok := sessionHelpPopup(sessionHelp, size.Cols-2, size.Rows-2)
	if !ok {
		return func() {}
	}
	boxW := pop.width()
	boxH := pop.height()

	startRow := (size.Rows-boxH)/2 + 1
	if startRow < 1 {
		startRow = 1
	}
	startCol := (size.Cols-boxW)/2 + 1
	if startCol < 1 {
		startCol = 1
	}

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

// sessionHelpBox renders the cheat-sheet as a bordered panel, one "label
// command" row per entry, no wider than maxWidth and no taller than maxRows. It
// returns equal-width plain-text lines (no ANSI), or nil when the panel cannot
// fit.
func sessionHelpBox(rows []sessionHelpRow, maxWidth, maxRows int) []string {
	pop, ok := sessionHelpPopup(rows, maxWidth, maxRows)
	if !ok {
		return nil
	}
	return pop.lines()
}

// sessionHelpPopup is sessionHelpBox's layout as a popup, ready to paint.
func sessionHelpPopup(rows []sessionHelpRow, maxWidth, maxRows int) (popup, bool) {
	if maxWidth < 20 || maxRows < len(rows)+2 || len(rows) == 0 {
		return popup{}, false
	}

	labelW := 0
	cmdW := 0
	for _, r := range rows {
		if n := vterm.StringWidth(r.label); n > labelW {
			labelW = n
		}
		if n := vterm.StringWidth(r.cmd); n > cmdW {
			cmdW = n
		}
	}

	width := 2 + labelW + 2 + cmdW + 2 // "│ " + label + "  " + cmd + " │"
	if width > maxWidth {
		width = maxWidth
		cmdW = width - 2 - labelW - 2 - 2
	}
	if cmdW < 8 {
		return popup{}, false
	}

	body := make([]popupRow, 0, len(rows))
	for _, r := range rows {
		body = append(body, popupRow{key: r.label, text: r.cmd})
	}
	return popup{
		title:  sessionHelpTitle,
		footer: "press any key",
		cols:   []popupCol{{rows: body, keyW: labelW, textW: cmdW}},
	}, true
}
