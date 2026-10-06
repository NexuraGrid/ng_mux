// Package config loads ngmux's optional configuration file. The format is one
// directive per line, '#' starts a comment:
//
//	set prefix C-a
//	set history-limit 5000
//	set default-shell /bin/bash
//	set mouse on
//	set escape-time 25
//	set theme light
//	set status-bg blue
//	set status-fg bright-white
//	set tab-active-bg 2
//	set border-active-fg magenta
//	set pane-titles off
//	bind s split-vertical
//	bind v split-horizontal
//
// Unknown directives are collected as warnings rather than failing the load, so
// a newer config never bricks an older binary.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/MauricioJC3/ng_mux/internal/theme"
	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

// DefaultPrefix is Ctrl-b, matching tmux.
const DefaultPrefix = 0x02

// Config is the resolved configuration. Zero value is the built-in default.
type Config struct {
	Prefix       byte              // control byte that begins a command sequence
	HistoryLimit int               // scrollback lines kept per pane
	DefaultShell string            // overrides $SHELL / platform default when set
	Mouse        bool              // reserved for a later phase
	EscapeTime   int               // ms to wait for a sequence after a lone Esc (tmux's escape-time)
	StatusFG     int               // status bar text colour (0..255 or theme.Default); -1 = the theme's
	StatusBG     int               // status bar background colour (0..255 or theme.Default); -1 = the theme's
	SetClipboard bool              // copy-mode yank also sets the OS clipboard via OSC 52
	Theme        string            // palette name for all chrome: dark, light or mono
	PaneTitles   bool              // a title line above each pane of a split window
	Binds        map[string]string // key (single rune) -> command name
	Colours      map[string]int    // per-element colour key (see colourKeys) -> colour

	// Warnings holds human-readable notes about lines that were ignored.
	Warnings []string
}

// Default returns the built-in configuration. Mouse support is on by default;
// disable it with `set mouse off` if you want native terminal selection.
// StatusFG/StatusBG start unset (-1) so the theme picks the bar's colours.
func Default() Config {
	return Config{
		Prefix:       DefaultPrefix,
		HistoryLimit: 2000,
		Mouse:        true,
		EscapeTime:   25,
		StatusFG:     -1,
		StatusBG:     -1,
		SetClipboard: true,
		Theme:        theme.DefaultName,
		PaneTitles:   true,
		Binds:        map[string]string{},
		Colours:      map[string]int{},
	}
}

// Path returns the location config is read from, honouring NGMUX_CONFIG then
// the per-OS user config directory.
func Path() string {
	if p := os.Getenv("NGMUX_CONFIG"); p != "" {
		return p
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ngmux", "ngmux.conf")
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "ngmux", "ngmux.conf")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "ngmux", "ngmux.conf")
}

// colourTarget is the palette colour a per-element colour key sets: a style
// and which side of it the user sees.
type colourTarget struct {
	style func(*theme.Palette) *theme.Style
	bg    bool // the visible background rather than the text
}

// colourKeys are the per-element colour settings, each overriding one colour
// of the active theme and leaving its attributes alone.
var colourKeys = map[string]colourTarget{
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

// Palette returns the theme the config names with any explicit status-fg /
// status-bg and per-element colours applied on top. An unknown theme name
// (which set already rejects) falls back to the default theme.
func (c Config) Palette() theme.Palette {
	pal, ok := theme.Lookup(c.Theme)
	if !ok {
		pal, _ = theme.Lookup(theme.DefaultName)
	}
	// The bar is drawn in reverse video, so the colour it visibly shows
	// behind its text is pal.StatusFG and its text colour is pal.StatusBG.
	if c.StatusFG >= 0 {
		pal.StatusBG = c.StatusFG
	}
	if c.StatusBG >= 0 {
		pal.StatusFG = c.StatusBG
	}
	for key, colour := range c.Colours {
		t, ok := colourKeys[key]
		if !ok {
			continue
		}
		st := t.style(&pal)
		// A reverse-video style shows its FG behind the text and its BG as
		// the text, so the visible sides swap.
		if t.bg != (st.Attr&vterm.AttrReverse != 0) {
			st.BG = colour
		} else {
			st.FG = colour
		}
	}
	return pal
}

// Load reads and parses Path(). A missing file is not an error: it returns the
// default config. A malformed line is recorded in Warnings and skipped.
func Load() (Config, error) {
	return LoadFile(Path())
}

// LoadFile is Load with an explicit path (used by tests).
func LoadFile(path string) (Config, error) {
	cfg := Default()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = strings.TrimSpace(text[:i])
		}
		fields := strings.Fields(text)
		if err := cfg.apply(fields); err != nil {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("line %d: %v", line, err))
		}
	}
	return cfg, sc.Err()
}

func (c *Config) apply(fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "set":
		if len(fields) < 3 {
			return fmt.Errorf("set needs a name and a value")
		}
		return c.set(fields[1], strings.Join(fields[2:], " "))
	case "bind":
		if len(fields) < 3 {
			return fmt.Errorf("bind needs a key and a command")
		}
		key := fields[1]
		if len([]rune(key)) != 1 {
			return fmt.Errorf("bind key must be a single character, got %q", key)
		}
		c.Binds[key] = fields[2]
		return nil
	default:
		return fmt.Errorf("unknown directive %q", fields[0])
	}
}

func (c *Config) set(name, value string) error {
	switch name {
	case "prefix":
		b, err := parseKey(value)
		if err != nil {
			return err
		}
		c.Prefix = b
	case "history-limit":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("history-limit must be a non-negative integer")
		}
		c.HistoryLimit = n
	case "default-shell":
		c.DefaultShell = value
	case "mouse":
		on, err := parseBool(value)
		if err != nil {
			return err
		}
		c.Mouse = on
	case "set-clipboard":
		on, err := parseBool(value)
		if err != nil {
			return err
		}
		c.SetClipboard = on
	case "escape-time":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("escape-time must be a non-negative integer (milliseconds)")
		}
		c.EscapeTime = n
	case "status-fg":
		n, err := parseColor(value)
		if err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
		c.StatusFG = n
	case "status-bg":
		n, err := parseColor(value)
		if err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
		c.StatusBG = n
	case "theme":
		if _, ok := theme.Lookup(value); !ok {
			return fmt.Errorf("theme must be one of %s, got %q", strings.Join(theme.Names(), ", "), value)
		}
		c.Theme = value
	case "pane-titles":
		on, err := parseBool(value)
		if err != nil {
			return err
		}
		c.PaneTitles = on
	default:
		if _, ok := colourKeys[name]; !ok {
			return fmt.Errorf("unknown setting %q", name)
		}
		n, err := parseColor(value)
		if err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
		c.Colours[name] = n
	}
	return nil
}

// parseKey turns "C-a", "C-b", "^A" or a bare single char into its control byte.
func parseKey(s string) (byte, error) {
	switch {
	case len(s) == 3 && (strings.HasPrefix(s, "C-") || strings.HasPrefix(s, "c-")):
		r := s[2]
		return ctrl(r)
	case len(s) == 2 && s[0] == '^':
		return ctrl(s[1])
	case len(s) == 1:
		return s[0], nil
	default:
		return 0, fmt.Errorf("cannot parse key %q (use C-a form)", s)
	}
}

func ctrl(r byte) (byte, error) {
	switch {
	case r >= 'a' && r <= 'z':
		return r - 'a' + 1, nil
	case r >= 'A' && r <= 'Z':
		return r - 'A' + 1, nil
	default:
		return 0, fmt.Errorf("C- must be followed by a letter, got %q", string(r))
	}
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("expected on/off, got %q", s)
	}
}

// colourNames are the eight base colour names, by xterm index.
var colourNames = []string{
	"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
}

// parseColor reads a colour value: an xterm index 0..255, one of the eight
// colour names (0-7), bright- plus a name (8-15), or "default" for the
// terminal's own colour (theme.Default). Names are case-insensitive.
func parseColor(s string) (int, error) {
	name := strings.ToLower(s)
	if name == "default" {
		return theme.Default, nil
	}
	base, bright := strings.CutPrefix(name, "bright-")
	for i, n := range colourNames {
		if base == n {
			if bright {
				return i + 8, nil
			}
			return i, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 255 {
		return 0, fmt.Errorf("colour must be 0..255, a colour name or default, got %q", s)
	}
	return n, nil
}
