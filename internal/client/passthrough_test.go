package client

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MauricioJC3/ng_mux/internal/protocol"
	"github.com/MauricioJC3/ng_mux/internal/theme"
)

// runInputEvents feeds keys through forwardInput and returns what reached the
// server, one string per message: "input:<bytes>", "exec:<line>" or
// "pass:<on|off>". Refreshes are dropped; they are repaint plumbing.
func runInputEvents(t *testing.T, km keymap, keys string) []string {
	t.Helper()
	term, err := os.CreateTemp(t.TempDir(), "term") // not a tty: no popup is drawn
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	ir := newInReader(strings.NewReader(keys))
	defer ir.close()
	cli, srv := net.Pipe()
	defer srv.Close()
	pal := theme.Dark()
	var overlay atomic.Bool
	go func() {
		forwardInput(ir, &lockedWriter{w: io.Discard}, term, protocol.NewConn(cli), km, &pal, 10*time.Millisecond, &overlay)
		cli.Close()
	}()

	var out []string
	pc := protocol.NewConn(srv)
	for {
		msg, err := pc.Read()
		if err != nil {
			return out
		}
		switch msg.Type {
		case protocol.TypeInput:
			out = append(out, fmt.Sprintf("input:%q", msg.Data))
		case protocol.TypeExec:
			out = append(out, "exec:"+msg.Name)
		case protocol.TypePassthrough:
			state := "off"
			if msg.N != 0 {
				state = "on"
			}
			out = append(out, "pass:"+state)
		}
	}
}

const f12 = "\x1b[24~"

func equalEvents(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// F12 turns passthrough on: the prefix and the key after it reach the pane as
// plain bytes. F12 again turns it off and the prefix works as before.
func TestF12TogglesPassthrough(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, f12+"\x02x"+f12+"\x02x")
	want := []string{
		"pass:on",
		`input:"\x02x"`,
		"pass:off",
		"exec:kill-pane",
	}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// F12 inside a chunk of typed bytes splits it: bytes before go out normally,
// the toggle happens, bytes after are forwarded in passthrough.
func TestF12InsideChunk(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, "ab"+f12+"c\x02d")
	want := []string{
		`input:"ab"`,
		"pass:on",
		`input:"c\x02d"`,
	}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// While passthrough is on, escape sequences other than F12 (arrows, other
// function keys) still reach the pane verbatim.
func TestPassthroughForwardsOtherEscapes(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, f12+"\x1b[A\x1b[23~")
	want := []string{"pass:on", `input:"\x1b[A"`, `input:"\x1b[23~"`}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// A config `bind P passthrough` turns it on from the prefix keymap.
func TestBindPassthroughCommand(t *testing.T) {
	km := keymap{prefix: DefaultPrefix, binds: map[string]string{"P": "passthrough"}}
	got := runInputEvents(t, km, "\x02P\x02c")
	want := []string{"pass:on", `input:"\x02c"`}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// Prefix twice still sends one literal prefix when passthrough is off.
func TestPrefixTwiceSendsLiteralPrefix(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, "\x02\x02")
	want := []string{`input:"\x02"`}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// Typing `passthrough` at the ':' prompt toggles the mode locally; the server
// never sees it as a command.
func TestPromptPassthroughCommand(t *testing.T) {
	got := runInputEvents(t, keymap{prefix: DefaultPrefix}, "\x02:passthrough\r\x02c")
	want := []string{"pass:on", `input:"\x02c"`}
	if !equalEvents(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}
