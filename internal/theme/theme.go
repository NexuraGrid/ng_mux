// Package theme holds ngmux's named colour palettes. One Palette styles every
// piece of chrome ngmux draws itself — the status bar, pane borders and
// titles, display-panes badges, the copy-mode indicator and the client's
// popups — so they read as one visual system and switch together.
//
// Palettes only use the 8 base colours plus bright-black (8), or no colour at
// all, so they render the same on a Linux console, an old conhost and a
// modern terminal.
package theme

import (
	"sort"

	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// Inherit, used as a status-bar Style FG or BG, keeps the bar's own colour on
// that side. It has no meaning outside the status bar.
const Inherit = -1

// Default is the terminal's own default colour (vterm.ColorDefault).
const Default = int(vterm.ColorDefault)

// Style is one piece of chrome's look: xterm colour indices (or Default /
// Inherit) plus vterm attribute bits (vterm.AttrBold, ...).
type Style struct {
	FG, BG int
	Attr   uint16
}

// Palette is a complete theme.
type Palette struct {
	// StatusFG / StatusBG are the bar's base colours. The bar draws them in
	// reverse video, so StatusFG is the colour the bar visibly shows behind
	// its text.
	StatusFG, StatusBG int

	Session Style // session-name pill
	Tab     Style // a window's tab that is neither current nor alerting
	Active  Style // the current window's tab
	Alert   Style // a hidden window with unseen output, and the overflow arrows pointing at one
	Mode    Style // ZOOM / COPY pills

	BorderDim    Style // dividers away from the focused pane (FG and Attr only)
	BorderActive Style // dividers around the focused pane (FG and Attr only)
	Title        Style // an unfocused pane's title text
	TitleActive  Style // the focused pane's title text
	Badge        Style // display-panes index on an unfocused pane
	BadgeActive  Style // display-panes index on the focused pane
	Overlay      Style // copy-mode position indicator

	PopupFrame Style // popup box-drawing frame
	PopupTitle Style // title embedded in the popup's top border
	PopupKey   Style // key column
	PopupText  Style // descriptions and footer
	PopupHead  Style // section heading inside a popup body
	PopupRule  Style // the dim rule that follows a section heading
}

const (
	bold      = vterm.AttrBold
	reverse   = vterm.AttrReverse
	underline = vterm.AttrUnderline
)

// Dark is the default look, for dark terminal backgrounds: a black bar, a
// blue session pill, green for "where you are" (active tab, focused pane) and
// yellow for anything needing attention.
func Dark() Palette {
	return Palette{
		StatusFG: 0, StatusBG: 7,
		Session: Style{FG: 0, BG: 4, Attr: bold},
		Tab:     Style{FG: Inherit, BG: Inherit},
		Active:  Style{FG: 0, BG: 2, Attr: bold},
		Alert:   Style{FG: 3, BG: Inherit, Attr: bold},
		Mode:    Style{FG: 0, BG: 3, Attr: bold},

		BorderDim:    Style{FG: 8, BG: Default},
		BorderActive: Style{FG: 2, BG: Default},
		Title:        Style{FG: Default, BG: Default},
		TitleActive:  Style{FG: 0, BG: 2, Attr: bold},
		Badge:        Style{FG: 0, BG: 4, Attr: bold},
		BadgeActive:  Style{FG: 0, BG: 2, Attr: bold},
		Overlay:      Style{FG: 0, BG: 3, Attr: bold},

		PopupFrame: Style{FG: 4, BG: Default},
		PopupTitle: Style{FG: 0, BG: 4, Attr: bold},
		PopupKey:   Style{FG: 2, BG: Default, Attr: bold},
		PopupText:  Style{FG: Default, BG: Default},
		PopupHead:  Style{FG: 4, BG: Default, Attr: bold},
		PopupRule:  Style{FG: 8, BG: Default},
	}
}

// Light suits light terminal backgrounds: a light-grey bar with black text,
// and dark accents (blue, magenta, red) instead of yellow or green text,
// which wash out on white.
func Light() Palette {
	return Palette{
		StatusFG: 7, StatusBG: 0,
		Session: Style{FG: 7, BG: 5, Attr: bold},
		Tab:     Style{FG: Inherit, BG: Inherit},
		Active:  Style{FG: 7, BG: 4, Attr: bold},
		Alert:   Style{FG: 1, BG: Inherit, Attr: bold},
		Mode:    Style{FG: 7, BG: 1, Attr: bold},

		BorderDim:    Style{FG: 8, BG: Default},
		BorderActive: Style{FG: 4, BG: Default},
		Title:        Style{FG: Default, BG: Default},
		TitleActive:  Style{FG: 7, BG: 4, Attr: bold},
		Badge:        Style{FG: 7, BG: 5, Attr: bold},
		BadgeActive:  Style{FG: 7, BG: 4, Attr: bold},
		Overlay:      Style{FG: 7, BG: 1, Attr: bold},

		PopupFrame: Style{FG: 4, BG: Default},
		PopupTitle: Style{FG: 7, BG: 4, Attr: bold},
		PopupKey:   Style{FG: 4, BG: Default, Attr: bold},
		PopupText:  Style{FG: Default, BG: Default},
		PopupHead:  Style{FG: 5, BG: Default, Attr: bold},
		PopupRule:  Style{FG: 8, BG: Default},
	}
}

// Mono uses no colour at all, only the terminal's default colours with bold,
// underline and reverse video, for monochrome terminals and for anyone who
// cannot tell the colour cues apart. The bar is reverse video; a pill is
// normal video inside it, so it still stands out.
func Mono() Palette {
	d := Style{FG: Default, BG: Default}
	return Palette{
		StatusFG: Default, StatusBG: Default,
		Session: Style{FG: Default, BG: Default, Attr: bold},
		Tab:     Style{FG: Inherit, BG: Inherit},
		Active:  Style{FG: Default, BG: Default, Attr: bold | underline},
		Alert:   Style{FG: Inherit, BG: Inherit, Attr: bold | underline},
		Mode:    Style{FG: Default, BG: Default, Attr: bold},

		BorderDim:    d,
		BorderActive: Style{FG: Default, BG: Default, Attr: bold},
		Title:        d,
		TitleActive:  Style{FG: Default, BG: Default, Attr: reverse | bold},
		Badge:        Style{FG: Default, BG: Default, Attr: reverse},
		BadgeActive:  Style{FG: Default, BG: Default, Attr: reverse | bold},
		Overlay:      Style{FG: Default, BG: Default, Attr: reverse | bold},

		PopupFrame: d,
		PopupTitle: Style{FG: Default, BG: Default, Attr: reverse | bold},
		PopupKey:   Style{FG: Default, BG: Default, Attr: bold},
		PopupText:  d,
		PopupHead:  Style{FG: Default, BG: Default, Attr: bold | underline},
		PopupRule:  d,
	}
}

// DefaultName is the theme used when the config names none.
const DefaultName = "dark"

var named = map[string]func() Palette{
	"dark":  Dark,
	"light": Light,
	"mono":  Mono,
}

// Lookup returns the palette called name and whether it exists.
func Lookup(name string) (Palette, bool) {
	f, ok := named[name]
	if !ok {
		return Palette{}, false
	}
	return f(), true
}

// Names lists the theme names in alphabetical order.
func Names() []string {
	out := make([]string, 0, len(named))
	for n := range named {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
