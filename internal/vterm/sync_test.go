package vterm

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func row0(term *Term) string {
	snap := term.Snapshot()
	var b strings.Builder
	for x := 0; x < snap.Cols; x++ {
		b.WriteRune(snap.At(x, 0).Ch)
	}
	return strings.TrimRight(b.String(), " ")
}

// While an app holds synchronized output (DEC mode 2026), snapshots keep
// showing the last complete frame; the new one appears in one piece when the
// app ends the update.
func TestSyncOutputShowsOnlyCompleteFrames(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("A"))
	if got := row0(term); got != "A" {
		t.Fatalf("before sync row = %q, want %q", got, "A")
	}
	term.Write([]byte("\x1b[?2026hBC"))
	if got := row0(term); got != "A" {
		t.Fatalf("mid-update row = %q, want the last complete frame %q", got, "A")
	}
	term.Write([]byte("D\x1b[?2026l"))
	if got := row0(term); got != "ABCD" {
		t.Fatalf("after update row = %q, want %q", got, "ABCD")
	}
}

// An app that never ends its update does not freeze the pane: after
// syncTimeout the live screen is shown again.
func TestSyncOutputTimesOut(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("A\x1b[?2026hB"))
	if got := row0(term); got != "A" {
		t.Fatalf("mid-update row = %q, want %q", got, "A")
	}
	time.Sleep(syncTimeout + 50*time.Millisecond)
	if got := row0(term); got != "AB" {
		t.Fatalf("after timeout row = %q, want the live screen %q", got, "AB")
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// DECRQM (CSI ? Ps $ p) for mode 2026 reports it as supported, so apps that
// ask before using it do use it.
func TestReportsSyncOutputSupport(t *testing.T) {
	var out syncBuf
	term := New(20, 2, &out)
	defer term.Close()
	term.Write([]byte("\x1b[?2026$p"))
	want := "\x1b[?2026;2$y"
	deadline := time.Now().Add(time.Second)
	for out.String() != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := out.String(); got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

// A full reset (RIS, ESC c) in the middle of a synchronized update ends it:
// the reset screen shows at once instead of after the timeout.
func TestResetEndsSyncOutput(t *testing.T) {
	term := New(20, 2, nil)
	term.Write([]byte("A\x1b[?2026h\x1bcZ"))
	if got := row0(term); got != "Z" {
		t.Fatalf("row after reset = %q, want %q", got, "Z")
	}
}
