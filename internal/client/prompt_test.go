package client

import (
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

// ',' and '$' open a prompt locally, so no default binding may claim them.
func TestQuickPromptKeysAreFree(t *testing.T) {
	km := keymap{prefix: DefaultPrefix}
	for key := range quickPrompts {
		if _, bound := defaultKeyCommands[key]; bound {
			t.Errorf("%q is both a quick prompt and a default binding", key)
		}
		if line, ok := km.resolveKey(key); ok {
			t.Errorf("resolveKey(%q) = %q, want unbound", key, line)
		}
	}
}

func TestQuickPromptBuildsRenameCommands(t *testing.T) {
	cases := []struct {
		key      byte
		input    string
		label    string
		wantLine string
	}{
		{',', "build", "rename window: ", "rename-window build"},
		{',', "  my tab  ", "rename window: ", "rename-window my tab"},
		{'$', "prod", "rename session: ", "rename-session prod"},
		{',', "", "rename window: ", ""},
		{'$', "   ", "rename session: ", ""},
	}
	for _, c := range cases {
		spec, ok := quickPrompts[c.key]
		if !ok {
			t.Fatalf("no quick prompt for %q", c.key)
		}
		if spec.label != c.label {
			t.Errorf("%q label = %q, want %q", c.key, spec.label, c.label)
		}
		if got := spec.build(c.input); got != c.wantLine {
			t.Errorf("%q build(%q) = %q, want %q", c.key, c.input, got, c.wantLine)
		}
	}
	if got := linePrompt.build("split-window -h"); got != "split-window -h" {
		t.Errorf("':' prompt build = %q, want the typed line", got)
	}
}

// runInput feeds keys through forwardInput and returns every command line it
// sent to the server, in order, once the input ends.
func runInput(t *testing.T, km keymap, keys string) []string {
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

	var lines []string
	pc := protocol.NewConn(srv)
	for {
		msg, err := pc.Read()
		if err != nil {
			return lines
		}
		if msg.Type == protocol.TypeExec {
			lines = append(lines, msg.Name)
		}
	}
}

func TestPrefixCommaRenamesWindow(t *testing.T) {
	got := runInput(t, keymap{prefix: DefaultPrefix}, "\x02,dev\r")
	if len(got) != 1 || got[0] != "rename-window dev" {
		t.Fatalf("sent %q, want [rename-window dev]", got)
	}
}

func TestPrefixDollarRenamesSession(t *testing.T) {
	got := runInput(t, keymap{prefix: DefaultPrefix}, "\x02$prod\x7fd\r")
	if len(got) != 1 || got[0] != "rename-session prod" {
		t.Fatalf("sent %q, want [rename-session prod]", got)
	}
}

// An empty name, or Esc, sends nothing at all.
func TestRenamePromptEmptyOrCancelledSendsNothing(t *testing.T) {
	if got := runInput(t, keymap{prefix: DefaultPrefix}, "\x02,\r\x02$  \r\x02,abc\x1b"); len(got) != 0 {
		t.Fatalf("sent %q, want nothing", got)
	}
}

// A config bind of ',' wins over the built-in prompt.
func TestConfigBindOverridesQuickPrompt(t *testing.T) {
	km := keymap{prefix: DefaultPrefix, binds: map[string]string{",": "new-window"}}
	got := runInput(t, km, "\x02,")
	if len(got) != 1 || got[0] != "new-window" {
		t.Fatalf("sent %q, want [new-window]", got)
	}
}
