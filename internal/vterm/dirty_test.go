package vterm

import (
	"sync"
	"testing"
	"time"
)

func TestDirtyStartsTrueAndClearsOnSnapshot(t *testing.T) {
	term := New(20, 5, nil)
	if !term.Dirty() {
		t.Fatal("a fresh Term should be dirty so its first frame is drawn")
	}
	term.Snapshot()
	if term.Dirty() {
		t.Fatal("Snapshot should clear the dirty flag")
	}
}

func TestDirtySetByWriteAndResize(t *testing.T) {
	term := New(20, 5, nil)
	term.Snapshot() // clean

	if _, err := term.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !term.Dirty() {
		t.Fatal("Write should mark the Term dirty")
	}

	term.Snapshot() // clean again
	term.Resize(30, 8)
	if !term.Dirty() {
		t.Fatal("Resize should mark the Term dirty")
	}
}

func TestSnapshotIntoReusesBufferAndMatchesSnapshot(t *testing.T) {
	term := New(12, 4, nil)
	term.Write([]byte("abcdef"))

	want := term.Snapshot()

	var dst Snapshot
	term.SnapshotInto(&dst)
	backing := &dst.Cells[0]

	if dst.Cols != want.Cols || dst.Rows != want.Rows || len(dst.Cells) != len(want.Cells) {
		t.Fatalf("SnapshotInto shape = %dx%d/%d, want %dx%d/%d",
			dst.Cols, dst.Rows, len(dst.Cells), want.Cols, want.Rows, len(want.Cells))
	}
	for i := range want.Cells {
		if dst.Cells[i] != want.Cells[i] {
			t.Fatalf("cell %d: SnapshotInto=%+v Snapshot=%+v", i, dst.Cells[i], want.Cells[i])
		}
	}

	term.Write([]byte("Z"))
	term.SnapshotInto(&dst)
	if &dst.Cells[0] != backing {
		t.Error("SnapshotInto reallocated its buffer instead of reusing it")
	}
}

// TestDirtyDoesNotBlockOnWriteLock is the whole point of making dirty an
// atomic.Bool: Dirty() must return promptly even while t.mu is held by a pane
// mid-Write, since the broadcaster polls every pane's Dirty() while holding
// its own session lock, and a block there would stall every other pane's
// input and every other session's frames behind one busy pane.
func TestDirtyDoesNotBlockOnWriteLock(t *testing.T) {
	term := New(20, 5, nil)
	term.mu.Lock()
	defer term.mu.Unlock()

	done := make(chan bool, 1)
	go func() { done <- term.Dirty() }()

	select {
	case got := <-done:
		if !got {
			t.Error("Dirty() = false while t.mu was held, want true (fresh Term)")
		}
	case <-time.After(time.Second):
		t.Fatal("Dirty() blocked while t.mu was held by another goroutine")
	}
}

// TestWriteRaceAgainstSnapshotStress hammers Write and Snapshot concurrently
// (run with -race to catch data races on the dirty flag itself) and then
// checks the basic invariant still holds afterward: Snapshot always leaves
// Dirty false, and a subsequent Write always leaves it true. Write and
// Snapshot both still serialize on t.mu, so a Write can never execute mid
// copy and leave bytes unaccounted for; this test is the regression guard for
// that property surviving the switch to a lock-free Dirty().
func TestWriteRaceAgainstSnapshotStress(t *testing.T) {
	if testing.Short() {
		t.Skip("race stress loop; skipped in -short")
	}
	term := New(20, 5, nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = term.Write([]byte("x"))
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				term.Snapshot()
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	term.Snapshot()
	if term.Dirty() {
		t.Fatal("Snapshot did not clear dirty after the stress loop")
	}
	if _, err := term.Write([]byte("y")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !term.Dirty() {
		t.Fatal("Write did not mark dirty after the stress loop")
	}
}
