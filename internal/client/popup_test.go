package client

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MauricioJC3/ng_mux/internal/theme"
)

func samplePopup() popup {
	return popup{
		title:  "keys",
		footer: "press a key",
		cols: []popupCol{{
			rows: []popupRow{
				{key: "c", text: "new window"},
				{key: "d", text: "detach"},
				{text: "+3 more", kind: rowSpan},
			},
			keyW:  3,
			textW: 12,
		}},
	}
}

// Every popup shares one frame: the title embedded at the left of the top
// border, the footer likewise at the bottom, all lines the same width.
func TestPopupLinesFrameAndTitle(t *testing.T) {
	lines := samplePopup().lines()
	want := []string{
		"┌─ keys ────────────┐",
		"│ c    new window   │",
		"│ d    detach       │",
		"│ +3 more           │",
		"└─ press a key ─────┘",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("popup lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	for i, l := range lines {
		if utf8.RuneCountInString(l) != samplePopup().width() {
			t.Errorf("line %d width = %d, want %d", i, utf8.RuneCountInString(l), samplePopup().width())
		}
	}
}

// A title longer than the frame is cut, never pushing the corner out.
func TestPopupLongTitleIsCut(t *testing.T) {
	p := samplePopup()
	p.title = strings.Repeat("x", 40)
	top := p.lines()[0]
	if utf8.RuneCountInString(top) != p.width() || !strings.HasSuffix(top, "─┐") {
		t.Fatalf("top border = %q, want it cut to width %d", top, p.width())
	}
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")

// paint draws the same text as lines, in the palette's popup styles: the key
// column in the accent colour, the title as a pill.
func TestPopupPaintUsesPalette(t *testing.T) {
	pal := theme.Dark()
	var b strings.Builder
	samplePopup().paint(&b, 3, 5, &pal)
	out := b.String()

	if !strings.Contains(out, "\x1b[3;5H") {
		t.Errorf("popup not positioned at row 3 col 5: %q", out)
	}
	if !strings.Contains(out, sgr(pal.PopupKey)+"c  ") {
		t.Errorf("key column not in the accent style: %q", out)
	}
	if !strings.Contains(out, sgr(pal.PopupTitle)+" keys ") {
		t.Errorf("title not drawn as a pill: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Errorf("paint must leave attributes reset: %q", out)
	}
	if got := ansiRE.ReplaceAllString(out, ""); got != strings.Join(samplePopup().lines(), "") {
		t.Errorf("painted text = %q, want the plain lines", got)
	}
}

// The mono theme's popups use no colour codes at all, and nothing uses the
// 256-colour palette in any theme.
func TestPopupPaintColourRange(t *testing.T) {
	for _, name := range theme.Names() {
		pal, _ := theme.Lookup(name)
		var b strings.Builder
		samplePopup().paint(&b, 1, 1, &pal)
		out := b.String()
		if strings.Contains(out, "38;5;") || strings.Contains(out, "48;5;") {
			t.Errorf("%s popup uses 256-colour codes: %q", name, out)
		}
		if name == "mono" && regexp.MustCompile(`;(3[0-7]|4[0-7]|9[0-7]|10[0-7])[;m]`).MatchString(out) {
			t.Errorf("mono popup sets a colour: %q", out)
		}
	}
}

func TestSGR(t *testing.T) {
	cases := []struct {
		st   theme.Style
		want string
	}{
		{theme.Style{FG: theme.Default, BG: theme.Default}, "\x1b[0;39;49m"},
		{theme.Style{FG: 0, BG: 4, Attr: 4 /* bold */}, "\x1b[0;1;30;44m"},
		{theme.Style{FG: 8, BG: theme.Inherit}, "\x1b[0;90;49m"},
	}
	for _, c := range cases {
		if got := sgr(c.st); got != c.want {
			t.Errorf("sgr(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
}

// A multi-column popup with headings: columns sit gap apart, a shorter column
// is padded, a heading is followed by a rule to the column width, and the note
// runs across the whole body.
func TestPopupColumnsHeadersAndNote(t *testing.T) {
	col := func(rows ...popupRow) popupCol { return popupCol{rows: rows, keyW: 1, textW: 6} }
	p := popup{
		title:  "t",
		footer: "f",
		gap:    2,
		cols: []popupCol{
			col(popupRow{text: "Ab", kind: rowHeader}, popupRow{key: "a", text: "alpha"}),
			col(popupRow{text: "Cd", kind: rowHeader}),
		},
		note: "+1 more",
	}
	want := []string{
		"┌─ t ──────────────────┐",
		"│ Ab ──────  Cd ────── │",
		"│ a  alpha             │",
		"│ +1 more              │",
		"└─ f ──────────────────┘",
	}
	got := p.lines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("popup lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p.height() != len(want) {
		t.Errorf("height = %d, want %d", p.height(), len(want))
	}

	pal := theme.Dark()
	var b strings.Builder
	p.paint(&b, 1, 1, &pal)
	out := b.String()
	if !strings.Contains(out, sgr(pal.PopupHead)+"Ab") || !strings.Contains(out, sgr(pal.PopupRule)+"──────") {
		t.Errorf("heading or rule not in its style: %q", out)
	}
	if got := ansiRE.ReplaceAllString(out, ""); got != strings.Join(want, "") {
		t.Errorf("painted text = %q, want the plain lines", got)
	}

	var e strings.Builder
	p.erase(&e, 4, 7)
	if n := strings.Count(e.String(), "H"+strings.Repeat(" ", p.width())); n != p.height() {
		t.Errorf("erase blanked %d lines, want %d", n, p.height())
	}
}

// Widths are display columns: a wide rune counts two and is never split.
func TestFitWidth(t *testing.T) {
	cases := []struct {
		s    string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 4, "abc…"},
		{"名前名前", 4, "名… "},
		{"↑ ←", 3, "↑ ←"},
		{"x", 0, ""},
	}
	for _, c := range cases {
		if got := fitWidth(c.s, c.w); got != c.want {
			t.Errorf("fitWidth(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
}
