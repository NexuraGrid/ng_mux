package vterm

import (
	"encoding/base64"
	"strings"
	"testing"
)

// An app copying with OSC 52 (Claude Code, nvim, tmux inside a pane) hands
// ngmux the text; TakeClipboard returns it once. Long payloads arrive whole
// (OSC strings used to be cut at 256 characters), and a clipboard read
// request ("?") is never answered or taken as text.
func TestInnerAppClipboard(t *testing.T) {
	term := New(20, 2, nil)
	if got := term.TakeClipboard(); got != "" {
		t.Fatalf("fresh terminal clipboard = %q, want empty", got)
	}

	term.Write([]byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hola mundo")) + "\a"))
	if got := term.TakeClipboard(); got != "hola mundo" {
		t.Fatalf("clipboard = %q, want %q", got, "hola mundo")
	}
	if got := term.TakeClipboard(); got != "" {
		t.Fatalf("second take = %q, want empty", got)
	}

	big := strings.Repeat("0123456789", 4000) // 40 KB, ~53 KB of base64
	term.Write([]byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(big)) + "\x1b\\"))
	if got := term.TakeClipboard(); got != big {
		t.Fatalf("big clipboard came back as %d bytes, want %d", len(got), len(big))
	}

	term.Write([]byte("\x1b]52;c;?\a"))
	if got := term.TakeClipboard(); got != "" {
		t.Fatalf("read request produced clipboard %q, want none", got)
	}
}

// A copy too big to hold whole is dropped rather than passed on cut short:
// half a clipboard is worse than none.
func TestOversizedClipboardIsDropped(t *testing.T) {
	term := New(20, 2, nil)
	huge := strings.Repeat("A", 160000) // more base64 than an OSC string holds
	term.Write([]byte("\x1b]52;c;" + huge + "\a"))
	if got := term.TakeClipboard(); got != "" {
		t.Fatalf("oversized copy produced %d bytes of clipboard, want none", len(got))
	}
	term.Write([]byte("\x1b]52;c;aGk=\a")) // the next copy still works
	if got := term.TakeClipboard(); got != "hi" {
		t.Fatalf("clipboard after an oversized copy = %q, want %q", got, "hi")
	}
}
