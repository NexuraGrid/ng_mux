package vterm

import (
	"bytes"
	"fmt"
	"testing"
)

// TestScrollbackCaptureAddsNoAllocationsAfterWarmup asserts that, once the
// scrollback ring is full (steady state for a long-lived pane), capturing a
// scrolled line reuses the ring's backing array instead of allocating one per
// line.
//
// It measures the *delta* between capture enabled (warmed up so the ring is
// full and every push overwrites) and capture disabled, rather than an
// absolute allocs/op count: vt10x's Write has its own pre-existing per-rune
// overhead (parse() unconditionally formats a debug string even when no
// DebugLogger is attached) that is unrelated to scrollback and out of scope
// here. The delta isolates what this change is responsible for.
func TestScrollbackCaptureAddsNoAllocationsAfterWarmup(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation microbenchmark; skipped in -short")
	}

	line := []byte("2026-09-13T12:00:00Z INFO worker processing job ok, some more realistic padding here\r\n")
	const warmupWrites = 2500
	const measureRuns = 500

	base := New(200, 50, nil)
	base.SetHistoryLimit(0) // capture disabled: baseline vt10x overhead only
	for i := 0; i < warmupWrites; i++ {
		if _, err := base.Write(line); err != nil {
			t.Fatalf("baseline warm-up write: %v", err)
		}
	}
	baseAllocs := testing.AllocsPerRun(measureRuns, func() {
		if _, err := base.Write(line); err != nil {
			t.Fatalf("baseline write: %v", err)
		}
	})

	captured := New(200, 50, nil)
	captured.SetHistoryLimit(2000) // capture enabled; warm-up fills the ring
	for i := 0; i < warmupWrites; i++ {
		if _, err := captured.Write(line); err != nil {
			t.Fatalf("capture warm-up write: %v", err)
		}
	}
	capturedAllocs := testing.AllocsPerRun(measureRuns, func() {
		if _, err := captured.Write(line); err != nil {
			t.Fatalf("capture write: %v", err)
		}
	})

	const tolerance = 0.5
	delta := capturedAllocs - baseAllocs
	if delta > tolerance {
		t.Fatalf("scrollback capture added %.3f allocs/op on top of the %.3f baseline (want delta <= %.3f): steady-state capture should reuse the ring's backing arrays",
			delta, baseAllocs, tolerance)
	}
}

// BenchmarkWriteLogFlood measures throughput feeding realistic, newline-
// terminated log output through a 200x50 pane with a 2000-line scrollback
// limit, in 32KB chunks (roughly a pty read buffer's worth).
func BenchmarkWriteLogFlood(b *testing.B) {
	term := New(200, 50, nil)
	term.SetHistoryLimit(2000)

	var buf bytes.Buffer
	lines := 0
	for buf.Len() < 32*1024 {
		fmt.Fprintf(&buf, "2026-09-13T12:00:00Z INFO worker-%d processing job %d ok\r\n", lines%8, lines)
		lines++
	}
	chunk := buf.Bytes()

	b.ReportAllocs()
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := term.Write(chunk); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
	b.StopTimer()
	if b.N > 0 {
		b.ReportMetric(float64(lines)*float64(b.N)/b.Elapsed().Seconds(), "lines/s")
	}
}
