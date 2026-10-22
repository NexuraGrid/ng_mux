package client

import "testing"

// A paste arrives from the terminal wrapped in ESC[200~ … ESC[201~. Its body
// is text, not keys: a Ctrl-b, an Esc or an F12 inside it must not trigger
// ngmux, and the whole paste, markers included, reaches the server as one
// input message so the server can decide what the pane receives.
func TestBracketedPasteIsForwardedLiterally(t *testing.T) {
	paste := "\x1b[200~line1 \x02x \x1b[24~ \x1b[A\rline2\x1b[201~"
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, paste+"\x02x")
	want := []string{
		`input:"\x1b[200~line1 \x02x \x1b[24~ \x1b[A\rline2\x1b[201~"`,
		"exec:kill-pane",
	}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}
