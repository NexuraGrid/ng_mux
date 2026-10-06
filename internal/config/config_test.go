package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/render"
	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

func writeConf(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ngmux.conf")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMissingFileIsDefault(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "nope.conf"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Prefix != DefaultPrefix || cfg.HistoryLimit != 2000 {
		t.Fatalf("expected defaults, got %+v", cfg)
	}
}

func TestParseFullConfig(t *testing.T) {
	p := writeConf(t, `
# a comment
set prefix C-a
set history-limit 9000
set default-shell /bin/bash
set mouse on
set escape-time 40
set status-fg 3
set status-bg 4
bind s split-vertical
bind v split-horizontal   # trailing comment
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", cfg.Warnings)
	}
	if cfg.Prefix != 0x01 {
		t.Errorf("prefix = %d, want 1 (C-a)", cfg.Prefix)
	}
	if cfg.HistoryLimit != 9000 {
		t.Errorf("history-limit = %d, want 9000", cfg.HistoryLimit)
	}
	if cfg.DefaultShell != "/bin/bash" {
		t.Errorf("default-shell = %q", cfg.DefaultShell)
	}
	if !cfg.Mouse {
		t.Errorf("mouse = false, want true")
	}
	if cfg.EscapeTime != 40 {
		t.Errorf("escape-time = %d, want 40", cfg.EscapeTime)
	}
	if cfg.StatusFG != 3 || cfg.StatusBG != 4 {
		t.Errorf("status colours = %d/%d, want 3/4", cfg.StatusFG, cfg.StatusBG)
	}
	if cfg.Binds["s"] != "split-vertical" || cfg.Binds["v"] != "split-horizontal" {
		t.Errorf("binds = %v", cfg.Binds)
	}
}

func TestMalformedLinesBecomeWarnings(t *testing.T) {
	p := writeConf(t, `
set prefix
set history-limit abc
frobnicate the widget
bind toolong split-vertical
set prefix C-x
`)
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(cfg.Warnings) != 4 {
		t.Fatalf("expected 4 warnings, got %d: %v", len(cfg.Warnings), cfg.Warnings)
	}
	// The last valid line still took effect.
	if cfg.Prefix != 0x18 {
		t.Errorf("prefix = %d, want 0x18 (C-x)", cfg.Prefix)
	}
}

func TestMouseDefaultsOnAndCanBeDisabled(t *testing.T) {
	def, _ := LoadFile(filepath.Join(t.TempDir(), "none.conf"))
	if !def.Mouse {
		t.Errorf("mouse should default on")
	}
	off, _ := LoadFile(writeConf(t, "set mouse off\n"))
	if off.Mouse {
		t.Errorf("`set mouse off` should disable mouse")
	}
}

func TestSetClipboardDefaultsOnAndCanBeDisabled(t *testing.T) {
	def, _ := LoadFile(filepath.Join(t.TempDir(), "none.conf"))
	if !def.SetClipboard {
		t.Errorf("set-clipboard should default on")
	}
	off, _ := LoadFile(writeConf(t, "set set-clipboard off\n"))
	if off.SetClipboard {
		t.Errorf("`set set-clipboard off` should disable it")
	}
}

func TestEscapeTimeDefaultsAndParses(t *testing.T) {
	def, _ := LoadFile(filepath.Join(t.TempDir(), "none.conf"))
	if def.EscapeTime != 25 {
		t.Errorf("escape-time default = %d, want 25", def.EscapeTime)
	}
	zero, _ := LoadFile(writeConf(t, "set escape-time 0\n"))
	if zero.EscapeTime != 0 {
		t.Errorf("`set escape-time 0` = %d, want 0", zero.EscapeTime)
	}
	bad, _ := LoadFile(writeConf(t, "set escape-time -3\n"))
	if len(bad.Warnings) != 1 {
		t.Errorf("negative escape-time should warn, got %v", bad.Warnings)
	}
	if bad.EscapeTime != 25 {
		t.Errorf("after a bad value, escape-time = %d, want the default 25", bad.EscapeTime)
	}
}

func TestParseKeyForms(t *testing.T) {
	cases := map[string]byte{"C-a": 1, "c-a": 1, "^A": 1, "C-b": 2, "q": 'q'}
	for in, want := range cases {
		got, err := parseKey(in)
		if err != nil || got != want {
			t.Errorf("parseKey(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

func TestThemeDefaultsDarkAndParses(t *testing.T) {
	def, _ := LoadFile(filepath.Join(t.TempDir(), "none.conf"))
	if def.Theme != "dark" {
		t.Errorf("theme default = %q, want dark", def.Theme)
	}
	if got, want := def.Palette(), theme.Dark(); got != want {
		t.Errorf("default palette = %+v, want the dark theme", got)
	}
	for _, name := range []string{"dark", "light", "mono"} {
		cfg, _ := LoadFile(writeConf(t, "set theme "+name+"\n"))
		if len(cfg.Warnings) != 0 || cfg.Theme != name {
			t.Errorf("`set theme %s` = %q, warnings %v", name, cfg.Theme, cfg.Warnings)
		}
	}
	bad, _ := LoadFile(writeConf(t, "set theme solarized\n"))
	if len(bad.Warnings) != 1 || bad.Theme != "dark" {
		t.Errorf("unknown theme should warn and keep dark, got %q / %v", bad.Theme, bad.Warnings)
	}
}

// An explicit status-fg / status-bg still wins over the theme's bar colours;
// without one the theme decides. The bar is reverse video, so status-fg (the
// visible text) lands in Palette.StatusBG and status-bg in Palette.StatusFG.
func TestPaletteAppliesStatusColourOverrides(t *testing.T) {
	mono, _ := LoadFile(writeConf(t, "set theme mono\n"))
	if got := mono.Palette(); got != theme.Mono() {
		t.Errorf("mono palette = %+v, want theme.Mono()", got)
	}
	cfg, _ := LoadFile(writeConf(t, "set theme mono\nset status-fg 3\nset status-bg 4\n"))
	pal := cfg.Palette()
	if pal.StatusFG != 4 || pal.StatusBG != 3 {
		t.Errorf("palette status FG/BG = %d/%d, want 4/3 (reverse video of fg 3 on bg 4)", pal.StatusFG, pal.StatusBG)
	}
	if pal.Active != theme.Mono().Active {
		t.Errorf("status overrides should not touch the rest of the theme: %+v", pal.Active)
	}
}

// What a user sets is what the bar shows: a padding cell of the bar displays
// status-bg behind status-fg text.
func TestStatusColoursRenderAsConfigured(t *testing.T) {
	for _, th := range []string{"dark", "light", "mono"} {
		cfg, _ := LoadFile(writeConf(t, "set theme "+th+"\nset status-fg bright-white\nset status-bg blue\n"))
		pal := cfg.Palette()
		f := render.ComposeThemedInto(nil, 10, 2, nil, nil, &pal)
		c := f.Cells[1*f.Cols+9] // last column of the bar: padding
		fg, bg := c.FG, c.BG
		if c.Attr&vterm.AttrReverse != 0 {
			fg, bg = bg, fg
		}
		if fg != 15 || bg != 4 {
			t.Errorf("%s: bar padding shows fg %d on bg %d, want 15 on 4", th, fg, bg)
		}
	}
}

func TestParseColorValues(t *testing.T) {
	cases := map[string]int{
		"0": 0, "7": 7, "255": 255,
		"black": 0, "red": 1, "green": 2, "yellow": 3,
		"blue": 4, "magenta": 5, "cyan": 6, "white": 7,
		"bright-black": 8, "bright-red": 9, "bright-white": 15,
		"Blue": 4, "BRIGHT-Cyan": 14,
		"default": theme.Default, "Default": theme.Default,
	}
	for in, want := range cases {
		got, err := parseColor(in)
		if err != nil || got != want {
			t.Errorf("parseColor(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"256", "-1", "purple", "bright-", "bright-default", "0x10", ""} {
		if _, err := parseColor(bad); err == nil {
			t.Errorf("parseColor(%q) should fail", bad)
		}
	}
}

func TestColourKeysParse(t *testing.T) {
	cfg, _ := LoadFile(writeConf(t, `
set session-fg black
set session-bg 5
set tab-fg bright-black
set tab-bg default
set tab-active-fg White
set tab-active-bg 2
set tab-alert-fg red
set tab-alert-bg 0
set mode-fg 0
set mode-bg yellow
set border-fg 8
set border-active-fg cyan
set pane-title-active-fg 0
set pane-title-active-bg bright-blue
`))
	if len(cfg.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", cfg.Warnings)
	}
	want := map[string]int{
		"session-fg": 0, "session-bg": 5, "tab-fg": 8, "tab-bg": theme.Default,
		"tab-active-fg": 7, "tab-active-bg": 2, "tab-alert-fg": 1, "tab-alert-bg": 0,
		"mode-fg": 0, "mode-bg": 3, "border-fg": 8, "border-active-fg": 6,
		"pane-title-active-fg": 0, "pane-title-active-bg": 12,
	}
	if len(cfg.Colours) != len(want) {
		t.Errorf("Colours = %v, want %v", cfg.Colours, want)
	}
	for k, v := range want {
		if cfg.Colours[k] != v {
			t.Errorf("%s = %d, want %d", k, cfg.Colours[k], v)
		}
	}
}

// A bad colour warns, naming the key and the value, and is ignored.
func TestBadColourValueWarns(t *testing.T) {
	cfg, _ := LoadFile(writeConf(t, "set tab-active-bg purple\nset status-fg 300\nset border-bg 4\n"))
	if len(cfg.Warnings) != 3 {
		t.Fatalf("expected 3 warnings, got %v", cfg.Warnings)
	}
	for i, want := range [][]string{{"tab-active-bg", "purple"}, {"status-fg", "300"}, {"border-bg"}} {
		for _, w := range want {
			if !strings.Contains(cfg.Warnings[i], w) {
				t.Errorf("warning %q should mention %q", cfg.Warnings[i], w)
			}
		}
	}
	if len(cfg.Colours) != 0 || cfg.StatusFG != -1 {
		t.Errorf("bad values should be ignored, got %v / status-fg %d", cfg.Colours, cfg.StatusFG)
	}
	if cfg.Palette() != theme.Dark() {
		t.Error("ignored colours should leave the theme untouched")
	}
}

// Each key changes only its own colour, over every theme, and keeps the
// theme's attributes.
func TestPaletteAppliesColourKeys(t *testing.T) {
	type field struct {
		style func(*theme.Palette) *theme.Style
		bg    bool
	}
	fields := map[string]field{
		"session-fg":           {func(p *theme.Palette) *theme.Style { return &p.Session }, false},
		"session-bg":           {func(p *theme.Palette) *theme.Style { return &p.Session }, true},
		"tab-fg":               {func(p *theme.Palette) *theme.Style { return &p.Tab }, false},
		"tab-bg":               {func(p *theme.Palette) *theme.Style { return &p.Tab }, true},
		"tab-active-fg":        {func(p *theme.Palette) *theme.Style { return &p.Active }, false},
		"tab-active-bg":        {func(p *theme.Palette) *theme.Style { return &p.Active }, true},
		"tab-alert-fg":         {func(p *theme.Palette) *theme.Style { return &p.Alert }, false},
		"tab-alert-bg":         {func(p *theme.Palette) *theme.Style { return &p.Alert }, true},
		"mode-fg":              {func(p *theme.Palette) *theme.Style { return &p.Mode }, false},
		"mode-bg":              {func(p *theme.Palette) *theme.Style { return &p.Mode }, true},
		"border-fg":            {func(p *theme.Palette) *theme.Style { return &p.BorderDim }, false},
		"border-active-fg":     {func(p *theme.Palette) *theme.Style { return &p.BorderActive }, false},
		"pane-title-active-fg": {func(p *theme.Palette) *theme.Style { return &p.TitleActive }, false},
		"pane-title-active-bg": {func(p *theme.Palette) *theme.Style { return &p.TitleActive }, true},
	}
	if len(fields) != len(colourKeys) {
		t.Fatalf("test covers %d keys, config has %d", len(fields), len(colourKeys))
	}
	for _, th := range theme.Names() {
		base, _ := theme.Lookup(th)
		for key, f := range fields {
			cfg, _ := LoadFile(writeConf(t, "set theme "+th+"\nset "+key+" 13\n"))
			got := cfg.Palette()
			want := base
			st := f.style(&want)
			// The colour the user sees on that side, even through reverse video.
			if f.bg != (st.Attr&vterm.AttrReverse != 0) {
				st.BG = 13
			} else {
				st.FG = 13
			}
			if got != want {
				t.Errorf("%s / %s: palette %+v, want %+v", th, key, got, want)
			}
			if f.style(&got).Attr != f.style(&base).Attr {
				t.Errorf("%s / %s changed the attributes", th, key)
			}
		}
	}
}

// Mono's focused title is reverse video: its visible background is FG.
func TestMonoReverseTitleOverridesVisibleSide(t *testing.T) {
	cfg, _ := LoadFile(writeConf(t, "set theme mono\nset pane-title-active-bg 4\n"))
	st := cfg.Palette().TitleActive
	if st.FG != 4 || st.BG != theme.Default {
		t.Errorf("mono title-active = %+v, want FG 4 (shown as background through reverse)", st)
	}
}

func TestPaneTitlesDefaultOnAndCanBeDisabled(t *testing.T) {
	def, _ := LoadFile(filepath.Join(t.TempDir(), "none.conf"))
	if !def.PaneTitles {
		t.Errorf("pane-titles should default on")
	}
	off, _ := LoadFile(writeConf(t, "set pane-titles off\n"))
	if off.PaneTitles {
		t.Errorf("`set pane-titles off` should disable them")
	}
	bad, _ := LoadFile(writeConf(t, "set pane-titles maybe\n"))
	if len(bad.Warnings) != 1 || !bad.PaneTitles {
		t.Errorf("a bad pane-titles value should warn and keep the default, got %v / %v", bad.PaneTitles, bad.Warnings)
	}
}

func TestResolvePath(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	existing := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, e := range paths {
				if p == e {
					return true
				}
			}
			return false
		}
	}
	macDir := func() (string, error) { return "/Users/u/Library/Application Support", nil }
	winDir := func() (string, error) { return `C:\Users\u\AppData\Roaming`, nil }
	appSupport := filepath.Join("/Users/u/Library/Application Support", "ngmux", "ngmux.conf")
	dotConfig := filepath.Join("/Users/u", ".config", "ngmux", "ngmux.conf")
	xdg := filepath.Join("/xdg", "ngmux", "ngmux.conf")

	cases := []struct {
		name   string
		goos   string
		vars   map[string]string
		exists func(string) bool
		dir    func() (string, error)
		want   string
	}{
		{"NGMUX_CONFIG wins", "darwin", map[string]string{"NGMUX_CONFIG": "/x.conf", "HOME": "/Users/u"}, existing(dotConfig), macDir, "/x.conf"},
		{"mac ~/.config when it exists", "darwin", map[string]string{"HOME": "/Users/u"}, existing(dotConfig, appSupport), macDir, dotConfig},
		{"XDG_CONFIG_HOME before ~/.config", "darwin", map[string]string{"HOME": "/Users/u", "XDG_CONFIG_HOME": "/xdg"}, existing(dotConfig, xdg), macDir, xdg},
		{"missing XDG file falls through", "darwin", map[string]string{"HOME": "/Users/u", "XDG_CONFIG_HOME": "/xdg"}, existing(dotConfig), macDir, dotConfig},
		{"relative XDG_CONFIG_HOME ignored", "darwin", map[string]string{"HOME": "/Users/u", "XDG_CONFIG_HOME": "xdg"}, existing(filepath.Join("xdg", "ngmux", "ngmux.conf")), macDir, appSupport},
		{"mac legacy Application Support", "darwin", map[string]string{"HOME": "/Users/u"}, existing(appSupport), macDir, appSupport},
		{"windows ignores XDG", "windows", map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/Users/u"}, existing(xdg, dotConfig), winDir, filepath.Join(`C:\Users\u\AppData\Roaming`, "ngmux", "ngmux.conf")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolvePath(c.goos, env(c.vars), c.exists, c.dir); got != c.want {
				t.Errorf("resolvePath = %q, want %q", got, c.want)
			}
		})
	}
}
