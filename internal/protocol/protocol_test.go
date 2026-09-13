package protocol

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// pipe is a minimal in-memory ReadWriteCloser backed by a bytes.Buffer.
type pipe struct{ buf bytes.Buffer }

func (p *pipe) Read(b []byte) (int, error)  { return p.buf.Read(b) }
func (p *pipe) Write(b []byte) (int, error) { return p.buf.Write(b) }
func (p *pipe) Close() error                { return nil }

func TestConnRoundTrip(t *testing.T) {
	p := &pipe{}
	c := NewConn(p)

	in := []Message{
		{Type: TypeAttach, Cols: 120, Rows: 40},
		{Type: TypeInput, Data: []byte("ls -la\r")},
		{Type: TypeCommand, Name: CmdSplitVertical},
		{Type: TypeListReply, Sessions: []SessionInfo{
			{Name: "work", Panes: 3, Attached: true},
			{Name: "scratch", Panes: 1},
		}},
	}
	for _, m := range in {
		if err := c.Write(m); err != nil {
			t.Fatalf("Write(%v): %v", m.Type, err)
		}
	}

	for i, want := range in {
		got, err := c.Read()
		if err != nil {
			t.Fatalf("Read #%d: %v", i, err)
		}
		if got.Type != want.Type || got.Cols != want.Cols || got.Rows != want.Rows ||
			got.Name != want.Name || !bytes.Equal(got.Data, want.Data) ||
			len(got.Sessions) != len(want.Sessions) {
			t.Fatalf("message #%d round-tripped wrong:\n got %+v\nwant %+v", i, got, want)
		}
	}
}

func TestReadRejectsOversizedHeader(t *testing.T) {
	p := &pipe{}
	// 0xFFFFFFFF length header, no body.
	p.buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	c := NewConn(p)
	if _, err := c.Read(); err == nil {
		t.Fatal("expected error on oversized frame header, got nil")
	}
}

func TestReadEOFOnEmptyStream(t *testing.T) {
	c := NewConn(&pipe{})
	if _, err := c.Read(); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

// TestConnConcurrentWrites exercises many goroutines writing distinct
// messages over the same Conn concurrently, with a single reader goroutine
// on the other end. Every message must arrive intact exactly once, and per
// writer order must be preserved. Large Data payloads make header/payload
// interleaving likely to surface as corruption if Write is not safe for
// concurrent use.
//
// This test fails against the pre-fix implementation (Conn.Write sharing a
// c.hdr field and issuing two separate rwc.Write calls): the run either
// hangs (a corrupted length header makes Read wait for a bogus byte count)
// or reports mismatched/garbled Data. See internal/protocol test notes in
// the PR description for the reproduction against origin/main.
func TestConnConcurrentWrites(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	writer := NewConn(clientConn)
	reader := NewConn(serverConn)

	const numWriters = 8
	const msgsPerWriter = 20
	large := make([]byte, 64<<10) // 64 KiB
	for i := range large {
		large[i] = byte(i)
	}

	totalMsgs := numWriters * msgsPerWriter

	// net.Pipe is synchronous (unbuffered): a Write blocks until a matching
	// Read, so the reader must run concurrently with the writers rather
	// than after they finish.
	type result struct {
		msg Message
		err error
	}
	results := make(chan result, totalMsgs)
	go func() {
		for i := 0; i < totalMsgs; i++ {
			m, err := reader.Read()
			results <- result{m, err}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < msgsPerWriter; i++ {
				name := fmt.Sprintf("writer-%d-msg-%d", w, i)
				m := Message{
					Type: TypeInput,
					Name: name,
					N:    w*1000 + i,
					Data: large,
				}
				if err := writer.Write(m); err != nil {
					t.Errorf("writer %d: Write #%d: %v", w, i, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	seen := make(map[string]int)
	lastSeq := make(map[int]int)
	for i := 0; i < totalMsgs; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("Read #%d: %v", i, r.err)
		}
		m := r.msg
		if len(m.Data) != len(large) {
			t.Fatalf("message %q: Data length = %d, want %d (corrupted frame)", m.Name, len(m.Data), len(large))
		}
		if !bytes.Equal(m.Data, large) {
			t.Fatalf("message %q: Data corrupted", m.Name)
		}
		seen[m.Name]++

		var wIdx, seq int
		if _, err := fmt.Sscanf(m.Name, "writer-%d-msg-%d", &wIdx, &seq); err != nil {
			t.Fatalf("message name %q did not parse: %v", m.Name, err)
		}
		if seq != lastSeq[wIdx] {
			t.Fatalf("writer %d: out-of-order message, got seq %d, want %d", wIdx, seq, lastSeq[wIdx])
		}
		lastSeq[wIdx] = seq + 1
	}

	if len(seen) != totalMsgs {
		t.Fatalf("got %d distinct messages, want %d", len(seen), totalMsgs)
	}
	for name, count := range seen {
		if count != 1 {
			t.Fatalf("message %q seen %d times, want 1", name, count)
		}
	}
}

// countingRWC wraps an io.ReadWriteCloser and counts Write calls, so tests
// can assert that Conn.Write issues exactly one underlying Write per
// message (header and payload combined) rather than one for the header and
// one for the payload.
type countingRWC struct {
	io.ReadWriteCloser
	writes atomic.Int64
}

func (c *countingRWC) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.ReadWriteCloser.Write(p)
}

func TestWriteIssuesOneUnderlyingWrite(t *testing.T) {
	p := &pipe{}
	counted := &countingRWC{ReadWriteCloser: p}
	c := NewConn(counted)

	if err := c.Write(Message{Type: TypeInput, Data: []byte("hello")}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := counted.writes.Load(); got != 1 {
		t.Fatalf("underlying Write called %d times, want 1", got)
	}

	// A second call adds exactly one more underlying Write.
	if err := c.Write(Message{Type: TypeInput, Data: []byte("world")}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := counted.writes.Load(); got != 2 {
		t.Fatalf("underlying Write called %d times after 2 messages, want 2", got)
	}
}
