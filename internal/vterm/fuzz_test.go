package vterm

import (
	"io"
	"testing"
)

// FuzzTermOps interleaves arbitrary pty payloads, split at arbitrary byte
// boundaries, with snapshots, scrollback views, resizes and history checks.
// It covers what the emulator-level fuzz targets cannot: the scrollback ring,
// the UTF-8 carry across writes, the reply queue and panic recovery. Any
// recovered emulator panic fails the run, since it points at a real bug.
func FuzzTermOps(f *testing.F) {
	f.Add([]byte("hello\r\nworld\r\nmore lines\r\n"), uint8(20), uint8(5))
	f.Add([]byte("\x1b[?1049h\x1b[2;4r\n\n\n\x1b[?1049l\xe4\xb8\x96\xe7\x95\x8c"), uint8(10), uint8(3))
	f.Add([]byte("\x1b[6n\x1b[c\x1b[>c\x1bD\x1b[3S\x1b[-5@\x1b[99999P"), uint8(1), uint8(1))
	f.Add([]byte("\xf0\x9f\x98\x80\xe4\xb8\x96\r\n\x1b[?1000h\x1b[?1006h\x1b[5T"), uint8(7), uint8(2))

	f.Fuzz(func(t *testing.T, data []byte, cols, rows uint8) {
		limit := int(rows % 64)
		term := New(int(cols%120)+1, int(rows%50)+1, io.Discard)
		defer term.Close()
		term.SetHistoryLimit(limit)

		for len(data) > 0 {
			n := 1 + int(data[0])%64
			if n > len(data) {
				n = len(data)
			}
			chunk := data[:n]
			data = data[n:]

			if _, err := term.Write(chunk); err != nil {
				t.Fatalf("Write(%q): %v", chunk, err)
			}

			switch chunk[0] % 4 {
			case 0:
				s := term.Snapshot()
				if s.CurX < 0 || s.CurX >= s.Cols || s.CurY < 0 || s.CurY >= s.Rows {
					t.Fatalf("cursor (%d,%d) outside a %dx%d screen", s.CurX, s.CurY, s.Cols, s.Rows)
				}
				if len(s.Cells) != s.Cols*s.Rows {
					t.Fatalf("snapshot has %d cells for %dx%d", len(s.Cells), s.Cols, s.Rows)
				}
			case 1:
				v := term.ScrollbackView(int(chunk[0]), int(rows%50)+1)
				if len(v.Cells) != v.Cols*v.Rows {
					t.Fatalf("scrollback view has %d cells for %dx%d", len(v.Cells), v.Cols, v.Rows)
				}
			case 2:
				term.Resize(int(chunk[0]%120)+1, n%50+1)
			case 3:
				if got := term.HistoryLen(); got > limit {
					t.Fatalf("history holds %d lines, limit is %d", got, limit)
				}
				_ = term.InputModes()
				_ = term.Dirty()
			}
		}
	})
}
