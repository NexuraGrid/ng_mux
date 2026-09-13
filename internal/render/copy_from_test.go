package render

import "testing"

func TestFrameCopyFrom(t *testing.T) {
	src := NewFrame(4, 2)
	src.Cells[5].Ch = 'x'
	src.CurX, src.CurY, src.CurVisible = 2, 1, true

	var dst Frame
	dst.CopyFrom(src)
	if dst.Cols != 4 || dst.Rows != 2 || dst.CurX != 2 || dst.CurY != 1 || !dst.CurVisible {
		t.Fatalf("copied header = %dx%d cur(%d,%d,%v), want 4x2 cur(2,1,true)",
			dst.Cols, dst.Rows, dst.CurX, dst.CurY, dst.CurVisible)
	}
	if dst.Cells[5].Ch != 'x' {
		t.Fatalf("cell 5 = %q, want 'x'", dst.Cells[5].Ch)
	}

	src.Cells[5].Ch = 'y'
	if dst.Cells[5].Ch != 'x' {
		t.Fatal("copy shares its cell buffer with the source")
	}

	if allocs := testing.AllocsPerRun(100, func() { dst.CopyFrom(src) }); allocs != 0 {
		t.Fatalf("CopyFrom at the same size allocated %.1f times, want 0", allocs)
	}

	bigger := NewFrame(6, 3)
	dst.CopyFrom(bigger)
	if dst.Cols != 6 || dst.Rows != 3 || len(dst.Cells) != 18 {
		t.Fatalf("after copying a 6x3 frame: %dx%d with %d cells", dst.Cols, dst.Rows, len(dst.Cells))
	}
}
