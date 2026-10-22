package client

import (
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

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

// Pasting into the ':' prompt types the pasted text into the prompt instead
// of cancelling it and sending the text to the pane.
func TestPasteIntoCommandPrompt(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, "\x02:\x1b[200~kill-pane\x1b[201~\r")
	want := []string{"exec:kill-pane"}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// A big paste is forwarded in bounded pieces (each well under the protocol's
// frame limit) that add up to the whole paste, markers included.
func TestLargePasteIsSentInBoundedChunks(t *testing.T) {
	body := strings.Repeat("0123456789abcdef", 20000) // 320 KB
	paste := "\x1b[200~" + body + "\x1b[201~"
	events := runInputEvents(t, keymap{prefix: DefaultPrefix}, paste)
	var all strings.Builder
	for _, e := range events {
		s, err := strconv.Unquote(strings.TrimPrefix(e, "input:"))
		if err != nil {
			t.Fatalf("unexpected event %q", e)
		}
		if len(s) > maxPasteChunk {
			t.Fatalf("chunk of %d bytes, want at most %d", len(s), maxPasteChunk)
		}
		all.WriteString(s)
	}
	if len(events) < 2 || all.String() != paste {
		t.Fatalf("got %d events totalling %d bytes, want several adding up to the %d-byte paste",
			len(events), all.Len(), len(paste))
	}
}

// A paste start whose end marker never arrives must not swallow the keyboard:
// once input goes quiet the paste is closed out and keys work again.
func TestUnterminatedPasteReleasesKeyboard(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("\x1b[200~abc"))
		time.Sleep(pasteIdleTimeout + 300*time.Millisecond)
		_, _ = pw.Write([]byte("\x02x"))
		pw.Close()
	}()
	got := runInputEventsFrom(t, keymap{prefix: DefaultPrefix}, pr)
	want := []string{`input:"\x1b[200~abc\x1b[201~"`, "exec:kill-pane"}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// A paste right after the prefix (which-key panel up) is not a command key:
// it is dropped whole, and none of it reaches the pane.
func TestPasteAfterPrefixIsDropped(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, "\x02\x1b[200~ls\r\x1b[201~\x02x")
	want := []string{"exec:kill-pane"}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// A paste while the Ctrl-b m sheet is up dismisses it like any key, and none
// of the pasted text reaches the pane.
func TestPasteDismissesSessionSheet(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, "\x02m\x1b[200~ls\r\x1b[201~\x02x")
	want := []string{"exec:kill-pane"}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// Arrow keys after the prefix still move focus.
func TestArrowAfterPrefixStillMovesFocus(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, "\x02\x1b[A\x02\x1b[C")
	want := []string{"exec:previous-pane", "exec:select-pane"}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// An arrow after the prefix that arrives split across reads (a slow link)
// still moves focus instead of leaking "[A" into the pane.
func TestSplitArrowAfterPrefixStillMovesFocus(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("\x02\x1b"))
		time.Sleep(10 * time.Millisecond)
		_, _ = pw.Write([]byte("[A"))
		pw.Close()
	}()
	got := runInputEventsFrom(t, keymap{prefix: DefaultPrefix}, pr)
	want := []string{"exec:previous-pane"}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// A paste closed out by the idle timeout still ends with the end marker, so
// an app in bracketed-paste mode leaves paste mode and later keys are keys.
func TestUnterminatedPasteIsClosedForTheApp(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("\x1b[200~abc"))
		time.Sleep(pasteIdleTimeout + 300*time.Millisecond)
		pw.Close()
	}()
	got := runInputEventsFrom(t, keymap{prefix: DefaultPrefix}, pr)
	want := []string{`input:"\x1b[200~abc\x1b[201~"`}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}
