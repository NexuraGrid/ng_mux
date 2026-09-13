package vterm

import (
	"testing"
)

func TestAnswersDeviceAttributeQueries(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		reply string
	}{
		{"primary DA", "\x1b[c", "\x1b[?1;2c"},
		{"primary DA explicit 0", "\x1b[0c", "\x1b[?1;2c"},
		{"secondary DA", "\x1b[>c", "\x1b[>84;0;0c"},
		{"secondary DA explicit 0", "\x1b[>0c", "\x1b[>84;0;0c"},
		{"query embedded in output", "done\x1b[cmore", "\x1b[?1;2c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reply syncBuffer
			term := New(20, 5, &reply)
			if _, err := term.Write([]byte(tc.in)); err != nil {
				t.Fatalf("Write: %v", err)
			}
			// Replies are delivered asynchronously by replyQueue's background
			// goroutine (see reply_queue.go), not synchronously inside Write.
			waitUntilReply(t, &reply, tc.reply)
		})
	}
}

func TestPlainOutputProducesNoReply(t *testing.T) {
	var reply syncBuffer
	term := New(20, 5, &reply)
	if _, err := term.Write([]byte("hello world\r\nsecond line\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// No query was ever recognized, so replyQ's delivery goroutine was never
	// started: there is nothing concurrently writing to reply, so a plain
	// synchronous read here is race-free.
	if reply.Len() != 0 {
		t.Fatalf("unexpected reply %q for plain output", reply.String())
	}
}

func TestDeviceQueryReplyDoesNotConsumeInput(t *testing.T) {
	var reply syncBuffer
	term := New(20, 5, &reply)
	in := []byte("AB\x1b[cCD")
	n, err := term.Write(in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(in) {
		t.Fatalf("Write returned n=%d, want %d (query must not shorten the write)", n, len(in))
	}
}

// TestDeviceAttributeQuerySplitAcrossWrites proves the DA query is recognized
// even when its bytes arrive in two separate Write calls. vt10x's own parser
// carries state across calls (see t.state in internal/vt10x/state.go), and
// answering DA from handleCSI's 'c' case (rather than scanning each raw
// payload with bytes.Contains, as vterm used to) means this "just works": the
// query is still one CSI sequence to the parser, regardless of where the
// caller happened to split it.
func TestDeviceAttributeQuerySplitAcrossWrites(t *testing.T) {
	var reply syncBuffer
	term := New(20, 5, &reply)
	if _, err := term.Write([]byte("\x1b[")); err != nil {
		t.Fatalf("Write (first half): %v", err)
	}
	if _, err := term.Write([]byte("c")); err != nil {
		t.Fatalf("Write (second half): %v", err)
	}
	waitUntilReply(t, &reply, "\x1b[?1;2c")

	// Answered exactly once: no double-delivery from any residual scanning
	// state.
	assertReplyStaysAt(t, &reply, "\x1b[?1;2c")
}

// TestSecondaryDADoesNotCorruptFollowingSequence proves the '>' prefix used by
// secondary DA is stripped like '?' is for private CSI sequences, rather than
// breaking argument parsing for whatever comes after it.
func TestSecondaryDADoesNotCorruptFollowingSequence(t *testing.T) {
	var reply syncBuffer
	term := New(20, 5, &reply)
	// Secondary DA followed by a cursor move that must still parse its arg.
	if _, err := term.Write([]byte("\x1b[>c\x1b[5C")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitUntilReply(t, &reply, "\x1b[>84;0;0c")

	term.mu.Lock()
	cur := term.t.Cursor()
	term.mu.Unlock()
	if cur.X != 5 {
		t.Fatalf("cursor.X = %d, want 5 (CUF after secondary DA must not be corrupted)", cur.X)
	}
}
