package mp4scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

// buildTiny writes a minimal ftyp+moov+mdat file and returns its bytes.
func buildTiny(t *testing.T) []byte {
	t.Helper()
	buf := &seekBuffer{}
	w := mp4.NewWriter(buf)
	ctx := mp4.Context{}
	start := func(typ mp4.BoxType, box mp4.IImmutableBox) {
		t.Helper()
		if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
			t.Fatal(err)
		}
		if box != nil {
			if _, err := mp4.Marshal(w, box, ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	end := func() {
		t.Helper()
		if _, err := w.EndBox(); err != nil {
			t.Fatal(err)
		}
	}
	start(mp4.BoxTypeFtyp(), &mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 1})
	end()
	start(mp4.BoxTypeMoov(), nil)
	start(mp4.BoxTypeFree(), nil)
	if _, err := w.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	end()
	end()
	start(mp4.BoxTypeMdat(), nil)
	if _, err := w.Write([]byte("media-bytes")); err != nil {
		t.Fatal(err)
	}
	end()
	return buf.b
}

// seekBuffer is an in-memory io.WriteSeeker.
type seekBuffer struct {
	b   []byte
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(len(s.b)) {
		nb := make([]byte, end)
		copy(nb, s.b)
		s.b = nb
	}
	copy(s.b[s.pos:], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		s.pos = offset
	case 1:
		s.pos += offset
	case 2:
		s.pos = int64(len(s.b)) + offset
	}
	return s.pos, nil
}

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanClean(t *testing.T) {
	data := buildTiny(t)
	res, err := Scan(context.Background(), writeFile(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if res.Diags.Count() != 0 {
		t.Fatalf("diags = %v", res.Diags)
	}
	if len(res.Find("moov")) != 1 || len(res.Find("mdat")) != 1 || len(res.Find("free")) != 1 {
		t.Fatalf("boxes = %+v", res.Boxes)
	}
	// mdat must be the last box, ending exactly at EOF.
	mdat := res.Find("mdat")[0]
	if mdat.End() != int64(len(data)) {
		t.Fatalf("mdat ends at %d, file is %d", mdat.End(), len(data))
	}
}

func TestScanTruncated(t *testing.T) {
	data := buildTiny(t)
	cut := len(data) - 5 // inside mdat payload
	res, err := Scan(context.Background(), writeFile(t, data[:cut]))
	if err != nil {
		t.Fatal(err)
	}
	if res.Diags.Count() == 0 {
		t.Fatal("expected truncation diagnostic")
	}
	d, ok := res.Diags.First()
	if !ok {
		t.Fatal("no diag")
	}
	if d.Offset <= 0 || d.Offset >= int64(cut) {
		t.Fatalf("diag offset %d not inside file (cut=%d)", d.Offset, cut)
	}
}

func TestScanTrailingData(t *testing.T) {
	data := append(buildTiny(t), 0xAA, 0xBB)
	res, err := Scan(context.Background(), writeFile(t, data))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range res.Diags {
		if d.Code == "trailing-data" && d.Offset == int64(len(data)-2) {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags = %v", res.Diags)
	}
}

func TestScanEmpty(t *testing.T) {
	res, err := Scan(context.Background(), writeFile(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Diags.HasFatal() {
		t.Fatalf("expected fatal diag, got %v", res.Diags)
	}
}
