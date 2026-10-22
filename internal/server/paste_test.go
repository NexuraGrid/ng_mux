package server

import (
	"testing"

	"github.com/MauricioJC3/ng_mux/internal/vterm"
)

const (
	testPasteStart = "\x1b[200~"
	testPasteEnd   = "\x1b[201~"
)

// A bracketed paste from the client keeps its markers only when the pane's
// app asked for bracketed paste; an app that did not would see them as
// garbage keys, so they are stripped and it gets the plain text.
func TestBracketedPasteMarkersFollowPaneMode(t *testing.T) {
	for _, tt := range []struct {
		name    string
		appWant bool
		want    string
	}{
		{"app without bracketed paste", false, "hello\nworld"},
		{"app with bracketed paste", true, testPasteStart + "hello\nworld" + testPasteEnd},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, ff, sess := setupSession(t)
			fp := ff.last()
			fp.scr.setInputModes(vterm.InputModes{BracketedPaste: tt.appWant})
			sess.input([]byte(testPasteStart + "hello\nworld" + testPasteEnd))
			if got := fp.pt.sentToChild(); got != tt.want {
				t.Fatalf("pane got %q, want %q", got, tt.want)
			}
		})
	}
}

// paste-buffer (Ctrl-b ]) wraps the buffer in paste markers when the pane's
// app asked for bracketed paste, so a multi-line buffer is inserted as text
// instead of each line being run as it arrives.
func TestPasteBufferIsBracketedWhenAppAsks(t *testing.T) {
	for _, tt := range []struct {
		name    string
		appWant bool
		want    string
	}{
		{"app without bracketed paste", false, "ls\nrm -rf x\n"},
		{"app with bracketed paste", true, testPasteStart + "ls\nrm -rf x\n" + testPasteEnd},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, ff, sess := setupSession(t)
			fp := ff.last()
			fp.scr.setInputModes(vterm.InputModes{BracketedPaste: tt.appWant})
			sess.mu.Lock()
			sess.pasteBuf = "ls\nrm -rf x\n"
			sess.mu.Unlock()
			exec(t, srv, "paste-buffer")
			if got := fp.pt.sentToChild(); got != tt.want {
				t.Fatalf("pane got %q, want %q", got, tt.want)
			}
		})
	}
}
