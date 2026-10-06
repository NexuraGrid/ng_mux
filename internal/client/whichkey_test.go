package client

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

var builtinTitles = []string{"Panes", "Windows (tabs)", "Sessions", "Copy & paste", "Other"}

// sameWidth fails unless every line has the first line's display width.
func sameWidth(t *testing.T, box []string) int {
	t.Helper()
	w := vterm.StringWidth(box[0])
	for i, line := range box {
		if got := vterm.StringWidth(line); got != w {
			t.Fatalf("line %d width = %d, want %d (%q)", i, got, w, line)
		}
	}
	return w
}

// headingAt reports the line and display column where a section heading (its
// title followed by " ─") starts, or -1, -1.
func headingAt(box []string, title string) (line, col int) {
	for i, l := range box {
		if j := strings.Index(l, title+" ─"); j >= 0 {
			return i, vterm.StringWidth(l[:j])
		}
	}
	return -1, -1
}

func TestWhichKeySectionsInOrder(t *testing.T) {
	box := whichKeyBox("Ctrl-b", whichKeySections(keymap{prefix: DefaultPrefix}), 60, 60)
	if box == nil {
		t.Fatal("no box at 60x60")
	}
	sameWidth(t, box)
	prev := -1
	for _, title := range builtinTitles {
		line, _ := headingAt(box, title)
		if line < 0 {
			t.Fatalf("section %q missing:\n%s", title, strings.Join(box, "\n"))
		}
		if line <= prev {
			t.Errorf("section %q at line %d, not after the previous one (%d)", title, line, prev)
		}
		prev = line
	}
	if !strings.Contains(box[0], "Ctrl-b") {
		t.Errorf("header missing prefix label: %q", box[0])
	}
	if !strings.Contains(box[len(box)-1], "press a key · Esc to cancel") {
		t.Errorf("footer missing the cancel hint: %q", box[len(box)-1])
	}
	joined := strings.Join(box, "\n")
	for _, want := range []string{"split top / bottom", "rename window", "rename session", "send a literal prefix"} {
		if !strings.Contains(joined, want) {
			t.Errorf("cheat-sheet missing %q", want)
		}
	}
	if strings.Contains(joined, whichKeyConfigTitle) {
		t.Errorf("no binds, yet a %q section:\n%s", whichKeyConfigTitle, joined)
	}
}

// Wide enough: sections sit in two columns, none split, and the panel is
// shorter than the one-column stack. Narrow: one column.
func TestWhichKeyTwoColumnsWhenWide(t *testing.T) {
	secs := whichKeySections(keymap{prefix: DefaultPrefix})

	wide := whichKeyBox("Ctrl-b", secs, 98, 29)
	if wide == nil {
		t.Fatal("no box at 98x29")
	}
	if w := sameWidth(t, wide); w > 98 {
		t.Fatalf("width %d exceeds 98", w)
	}
	cols := map[int]bool{}
	for _, title := range builtinTitles {
		_, col := headingAt(wide, title)
		if col < 0 {
			t.Fatalf("section %q missing in the wide box:\n%s", title, strings.Join(wide, "\n"))
		}
		cols[col] = true
	}
	if len(cols) != 2 {
		t.Errorf("headings start in %d distinct columns, want 2:\n%s", len(cols), strings.Join(wide, "\n"))
	}
	if strings.Contains(strings.Join(wide, ""), "more") {
		t.Errorf("wide box should hold every row:\n%s", strings.Join(wide, "\n"))
	}

	narrow := whichKeyBox("Ctrl-b", secs, 50, 60)
	if narrow == nil {
		t.Fatal("no box at 50x60")
	}
	sameWidth(t, narrow)
	cols = map[int]bool{}
	for _, title := range builtinTitles {
		_, col := headingAt(narrow, title)
		cols[col] = true
	}
	if len(cols) != 1 {
		t.Errorf("narrow box headings in %d columns, want 1:\n%s", len(cols), strings.Join(narrow, "\n"))
	}
	if len(wide) >= len(narrow) {
		t.Errorf("two columns (%d lines) should be shorter than one (%d)", len(wide), len(narrow))
	}
}

// The classic 80x24 terminal still gets every row, in two columns.
func TestWhichKeyFits80x24(t *testing.T) {
	box := whichKeyBox("Ctrl-b", whichKeySections(keymap{prefix: DefaultPrefix}), 78, 23)
	if box == nil || strings.Contains(strings.Join(box, ""), "more") {
		t.Fatalf("80x24 should show the whole cheat-sheet:\n%s", strings.Join(box, "\n"))
	}
}

func TestWhichKeyBoxCapsRows(t *testing.T) {
	secs := whichKeySections(keymap{prefix: DefaultPrefix})
	box := whichKeyBox("Ctrl-b", secs, 40, 10)
	if len(box) != 10 {
		t.Fatalf("line count = %d, want 10 (the maxRows cap):\n%s", len(box), strings.Join(box, "\n"))
	}
	sameWidth(t, box)
	note := box[len(box)-2]
	m := regexp.MustCompile(`\+(\d+) more`).FindStringSubmatch(note)
	if m == nil {
		t.Fatalf("expected a '+N more' line before the footer, got %q", note)
	}
	// Rows are dropped from the end: the first section is still there and
	// the shown entries plus N account for every row.
	if line, _ := headingAt(box, "Panes"); line != 1 {
		t.Errorf("first section should lead the panel:\n%s", strings.Join(box, "\n"))
	}
	total, shown := 0, 0
	for _, s := range secs {
		total += len(s.rows)
	}
	for _, l := range box[1 : len(box)-2] {
		if strings.HasPrefix(l, "│ ") && !strings.Contains(l, " ─") && strings.TrimSpace(strings.Trim(l, "│")) != "" {
			shown++
		}
	}
	if got := m[1]; got != strconv.Itoa(total-shown) {
		t.Errorf("note says +%s more, want +%d (%d rows, %d shown)", got, total-shown, total, shown)
	}
}

func TestWhichKeyBoxTooSmall(t *testing.T) {
	secs := whichKeySections(keymap{prefix: DefaultPrefix})
	if box := whichKeyBox("Ctrl-b", secs, 10, 40); box != nil {
		t.Fatalf("want nil for a too-narrow panel, got %d lines", len(box))
	}
	if box := whichKeyBox("Ctrl-b", secs, 60, 2); box != nil {
		t.Fatalf("want nil for a too-short panel, got %d lines", len(box))
	}
}

func TestWhichKeyConfigSectionOnlyWithBinds(t *testing.T) {
	km := keymap{prefix: DefaultPrefix, binds: map[string]string{"S": "split-window -v"}}
	secs := whichKeySections(km)
	last := secs[len(secs)-1]
	if last.title != whichKeyConfigTitle || len(last.rows) != 1 || last.rows[0].keys != "S" {
		t.Fatalf("last section = %+v, want the user's S binding", last)
	}
	box := whichKeyBox("Ctrl-b", secs, 100, 40)
	if line, _ := headingAt(box, whichKeyConfigTitle); line < 0 {
		t.Fatalf("config section missing:\n%s", strings.Join(box, "\n"))
	}
	if !strings.Contains(strings.Join(box, "\n"), "split-window -v") {
		t.Errorf("config binding not listed:\n%s", strings.Join(box, "\n"))
	}
	if n := len(whichKeySections(keymap{prefix: DefaultPrefix})); n != len(builtinWhichKey) {
		t.Errorf("no binds: %d sections, want %d", n, len(builtinWhichKey))
	}
}

func TestWhichKeyUsesCustomPrefixLabel(t *testing.T) {
	secs := whichKeySections(keymap{prefix: 0x01})
	box := strings.Join(whichKeyBox(prefixLabel(0x01), secs, 100, 40), "\n")
	if !regexp.MustCompile(`Ctrl-a +send a literal prefix`).MatchString(box) || strings.Contains(box, whichKeyPrefixKey) {
		t.Errorf("prefix row not relabelled for Ctrl-a:\n%s", box)
	}
}

// Every built-in description fits the column budget that keeps two columns
// on an 80-column terminal.
func TestWhichKeyDescriptionsAreShort(t *testing.T) {
	for _, s := range builtinWhichKey {
		for _, r := range s.rows {
			if w := vterm.StringWidth(r.desc); w > whichKeyMaxDescW {
				t.Errorf("%s / %q: description %q is %d columns, max %d", s.title, r.keys, r.desc, w, whichKeyMaxDescW)
			}
		}
	}
}

// The mono theme paints the cheat-sheet with attributes only: the only colour
// codes are the terminal defaults (39 / 49).
func TestWhichKeyMonoPaintHasNoColour(t *testing.T) {
	pop, ok := whichKeyPopup("Ctrl-b", whichKeySections(keymap{prefix: DefaultPrefix}), 98, 29)
	if !ok {
		t.Fatal("no popup")
	}
	pal := theme.Mono()
	var b strings.Builder
	pop.paint(&b, 1, 1, &pal)
	for _, m := range regexp.MustCompile("\x1b\\[([0-9;]*)m").FindAllStringSubmatch(b.String(), -1) {
		for _, p := range strings.Split(m[1], ";") {
			switch p {
			case "", "0", "1", "4", "7", "39", "49":
			default:
				t.Fatalf("mono paint uses SGR %q in %q", p, m[0])
			}
		}
	}
	if got := ansiRE.ReplaceAllString(b.String(), ""); got != strings.Join(pop.lines(), "") {
		t.Errorf("painted text differs from lines()")
	}
}

func TestPrefixLabel(t *testing.T) {
	cases := map[byte]string{0x02: "Ctrl-b", 0x01: "Ctrl-a", ' ': " ", '~': "~"}
	for in, want := range cases {
		if got := prefixLabel(in); got != want {
			t.Errorf("prefixLabel(%#x) = %q, want %q", in, got, want)
		}
	}
}
