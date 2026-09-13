package vterm

import "testing"

// historySnapshotRows reads every currently retained history row (oldest
// first) as trimmed text, for tests that need to inspect the ring directly
// rather than through ScrollbackView's history+live compositing.
func historySnapshotRows(term *Term) []string {
	term.mu.Lock()
	defer term.mu.Unlock()
	rows := make([]string, term.histLen)
	for i := 0; i < term.histLen; i++ {
		rows[i] = cellRowText(term.historyRow(i))
	}
	return rows
}

func cellRowText(row []Cell) string {
	b := make([]rune, 0, len(row))
	for _, c := range row {
		if c.Ch == 0 {
			b = append(b, ' ')
		} else {
			b = append(b, c.Ch)
		}
	}
	s := string(b)
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}

// TestRepeatedIdenticalLinesExactHistory guards against the old diff-based
// heuristic's bug: scrollAmount picked the largest k whose rows matched, so
// identical consecutive lines got duplicated into history (30 lines through a
// 40x10 screen used to give HistoryLen()==168 instead of 21). With capture
// wired to vt10x's own scroll events, one write == one push, always.
func TestRepeatedIdenticalLinesExactHistory(t *testing.T) {
	term := New(40, 10, nil)
	term.SetHistoryLimit(1000)
	for i := 0; i < 30; i++ {
		if _, err := term.Write([]byte("heartbeat ok\r\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := term.HistoryLen(); got != 21 {
		t.Fatalf("HistoryLen() = %d, want 21 (30 lines - (10 rows - 1))", got)
	}
	for i, row := range historySnapshotRows(term) {
		if row != "heartbeat ok" {
			t.Fatalf("history[%d] = %q, want %q", i, row, "heartbeat ok")
		}
	}
}

func TestRingWrapKeepsNewestInOrder(t *testing.T) {
	term := New(20, 5, nil) // 5 visible rows
	term.SetHistoryLimit(6)
	for i := 0; i < 40; i++ {
		term.Write([]byte("l" + itoa(i) + "\r\n"))
	}
	// 40 lines through a 5-row screen scroll lines l0..l35 (36 of them) into
	// the ring; capacity 6 keeps only the newest 6, oldest first.
	rows := historySnapshotRows(term)
	want := []string{"l30", "l31", "l32", "l33", "l34", "l35"}
	if len(rows) != len(want) {
		t.Fatalf("HistoryLen() = %d, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i] != w {
			t.Fatalf("history[%d] = %q, want %q", i, rows[i], w)
		}
	}
}

func TestSetHistoryLimitShrinkGrowPreservesNewest(t *testing.T) {
	term := New(20, 4, nil)
	term.SetHistoryLimit(100)
	for i := 0; i < 20; i++ {
		term.Write([]byte("x" + itoa(i) + "\r\n"))
	}
	if got := term.HistoryLen(); got != 17 {
		t.Fatalf("setup HistoryLen() = %d, want 17", got)
	}

	term.SetHistoryLimit(5) // shrink: keep only the newest 5
	want := []string{"x12", "x13", "x14", "x15", "x16"}
	if rows := historySnapshotRows(term); !equalRows(rows, want) {
		t.Fatalf("after shrink history = %v, want %v", rows, want)
	}

	term.SetHistoryLimit(50) // grow: retained rows survive, unchanged, in order
	if rows := historySnapshotRows(term); !equalRows(rows, want) {
		t.Fatalf("after grow history = %v, want %v (growing must not fabricate lost history)", rows, want)
	}
}

func equalRows(got, want []string) bool {
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

func TestAltScreenWritesDoNotCaptureHistory(t *testing.T) {
	term := New(20, 4, nil)
	term.SetHistoryLimit(100)
	term.Write([]byte("\x1b[?1049h")) // enter alternate screen
	for i := 0; i < 20; i++ {
		term.Write([]byte("a" + itoa(i) + "\r\n"))
	}
	if got := term.HistoryLen(); got != 0 {
		t.Fatalf("HistoryLen() = %d, want 0: the alternate screen must never populate scrollback", got)
	}
}

func TestScrolledTotalCountsPushedLines(t *testing.T) {
	term := New(20, 4, nil)
	term.SetHistoryLimit(3) // small cap: eviction starts well before writes stop
	for i := 0; i < 20; i++ {
		term.Write([]byte("z" + itoa(i) + "\r\n"))
	}
	if got := term.ScrolledTotal(); got != 17 {
		t.Fatalf("ScrolledTotal() = %d, want 17 (20 lines - (4 rows - 1)), independent of the 3-line cap", got)
	}
	if got := term.HistoryLen(); got != 3 {
		t.Fatalf("HistoryLen() = %d, want 3 (capped)", got)
	}
}

func TestScrolledTotalZeroWhenCaptureDisabled(t *testing.T) {
	term := New(20, 4, nil)
	term.SetHistoryLimit(0)
	for i := 0; i < 20; i++ {
		term.Write([]byte("z" + itoa(i) + "\r\n"))
	}
	if got := term.ScrolledTotal(); got != 0 {
		t.Fatalf("ScrolledTotal() = %d, want 0 while capture is disabled", got)
	}
}

func TestWideGlyphWidthSurvivesInHistory(t *testing.T) {
	term := New(10, 2, nil) // 2 rows: the very next linefeed at the bottom scrolls
	term.SetHistoryLimit(10)
	term.Write([]byte("名a\r\n"))
	term.Write([]byte("second\r\n")) // scrolls the wide-glyph row into history

	term.mu.Lock()
	row := append([]Cell(nil), term.historyRow(0)...)
	term.mu.Unlock()

	if row[0].Ch != '名' || row[0].Width != 2 {
		t.Fatalf("history cell(0) = %#U width=%d, want '名' width 2", row[0].Ch, row[0].Width)
	}
	if row[1].Width != 0 {
		t.Fatalf("history cell(1) width = %d, want 0 (spacer after a wide glyph)", row[1].Width)
	}
}

// TestPanicRecoveryKeepsCapturingScrollback forces an emulator panic (see
// forcePanicOnNextWrite); Write recovers and swaps in a fresh emulator. That
// fresh emulator must be wired to the same scrollback hook, or capture
// silently stops forever after the first recovered panic.
func TestPanicRecoveryKeepsCapturingScrollback(t *testing.T) {
	term := New(20, 4, nil)
	term.SetHistoryLimit(100)

	for i := 0; i < 20; i++ {
		if _, err := writeWithTimeout(t, term, []byte("p"+itoa(i)+"\r\n")); err != nil {
			t.Fatalf("setup write %d: %v", i, err)
		}
	}
	before := term.HistoryLen()
	if before == 0 {
		t.Fatal("setup: expected some scrollback before forcing a panic")
	}

	forcePanicOnNextWrite(term)
	if _, err := writeWithTimeout(t, term, []byte("boom")); err == nil {
		t.Fatal("expected the forced panic to surface as an error")
	}

	for i := 0; i < 20; i++ {
		if _, err := writeWithTimeout(t, term, []byte("q"+itoa(i)+"\r\n")); err != nil {
			t.Fatalf("post-recovery write %d: %v", i, err)
		}
	}
	if got := term.HistoryLen(); got <= before {
		t.Fatalf("HistoryLen() = %d after recovery writes, want > %d: the fresh emulator must keep capturing", got, before)
	}
}
