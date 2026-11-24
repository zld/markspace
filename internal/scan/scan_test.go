package scan

import (
	"testing"

	"archivist/internal/fault"
	"archivist/internal/fixture"
)

func build(t *testing.T, dir, kind string) *fixture.Sample {
	t.Helper()
	opts := fixture.DefaultOptions(kind, kind)
	opts.SampleCount = 20
	switch kind {
	case fixture.KindTruncated, fixture.KindTruncatedHeader, fixture.KindBadLength:
		s, err := fixture.BuildCorrupt(dir, opts, kind)
		if err != nil {
			t.Fatal(err)
		}
		return s
	default:
		s, err := fixture.Build(dir, opts)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func TestScanNormal(t *testing.T) {
	dir := t.TempDir()
	s := build(t, dir, fixture.KindNormal)
	r, err := Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c, err := r.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if c.MovieTimescale != 1000 {
		t.Fatalf("movie timescale = %d", c.MovieTimescale)
	}
	if len(c.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(c.Tracks))
	}
	tr := c.Tracks[0]
	if tr.Timescale != 90000 || tr.ID != 1 || tr.Handler != "vide" {
		t.Fatalf("track = %+v", tr)
	}
	if len(tr.Samples) != 20 {
		t.Fatalf("samples = %d", len(tr.Samples))
	}
	for i, smp := range tr.Samples {
		wantDTS := int64(i) * 3000
		if smp.DTS != wantDTS {
			t.Fatalf("sample %d DTS = %d want %d", i, smp.DTS, wantDTS)
		}
		if smp.PTS != wantDTS {
			t.Fatalf("sample %d PTS = %d", i, smp.PTS)
		}
		if smp.Index != i+1 || smp.Size != 4 {
			t.Fatalf("sample %d metadata = %+v", i, smp)
		}
		wantKey := i%15 == 0
		if smp.Keyframe != wantKey {
			t.Fatalf("sample %d keyframe = %v want %v", i, smp.Keyframe, wantKey)
		}
	}
	// Sample payloads must land inside mdat.
	var mdat BoxRef
	for _, b := range c.Boxes {
		if b.Type == "mdat" {
			mdat = b
		}
	}
	if mdat.Size == 0 {
		t.Fatal("mdat not found")
	}
	for _, smp := range tr.Samples {
		if smp.Offset < mdat.Offset+int64(mdat.HeaderSize) || smp.Offset+int64(smp.Size) > mdat.End() {
			t.Fatalf("sample offset %d outside mdat [%d,%d]", smp.Offset, mdat.Offset, mdat.End())
		}
	}
}

func TestScanSkipsUnknownBoxes(t *testing.T) {
	dir := t.TempDir()
	s := build(t, dir, fixture.KindNormal)
	r, err := Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c, err := r.Scan()
	if err != nil {
		t.Fatal(err)
	}
	var sawFree, sawUnknown bool
	for _, b := range c.Skipped {
		switch b.Type {
		case "free":
			sawFree = true
		case "zzzz":
			sawUnknown = true
		}
	}
	if !sawFree || !sawUnknown {
		t.Fatalf("skipped = %+v", c.Skipped)
	}
	if len(c.Warnings) == 0 {
		t.Fatal("expected a warning for the unknown box")
	}
}

func TestScanCorruptOffsets(t *testing.T) {
	cases := []struct {
		kind string
	}{
		{fixture.KindTruncated},
		{fixture.KindTruncatedHeader},
		{fixture.KindBadLength},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			dir := t.TempDir()
			s := build(t, dir, tc.kind)
			r, err := Open(s.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			_, err = r.Scan()
			if err == nil {
				t.Fatal("expected corruption")
			}
			f, ok := fault.AsFault(err)
			if !ok {
				t.Fatalf("not a fault: %v", err)
			}
			if f.Kind != fault.KindCorrupt {
				t.Fatalf("kind = %s", f.Kind)
			}
			if f.Offset != s.CorruptAt {
				t.Fatalf("offset = %d want %d (%v)", f.Offset, s.CorruptAt, err)
			}
			if code := fault.ExitCode(err); code != 2 {
				t.Fatalf("exit = %d", code)
			}
		})
	}
}

func TestScanTruncatedHeaderInsideParent(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("cut", fixture.KindTruncated)
	opts.SampleCount = 10
	s, err := fixture.BuildCorrupt(dir, opts, fixture.KindTruncated)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.Scan()
	f, ok := fault.AsFault(err)
	if !ok || f.Kind != fault.KindCorrupt {
		t.Fatalf("err = %v", err)
	}
	if f.Offset != s.CorruptAt {
		t.Fatalf("offset = %d want %d", f.Offset, s.CorruptAt)
	}
}

func TestScanMissingFile(t *testing.T) {
	_, err := Open("/nonexistent/archivist.mp4")
	if err == nil {
		t.Fatal("expected error")
	}
	if fault.ExitCode(err) != 1 {
		t.Fatalf("exit = %d", fault.ExitCode(err))
	}
}

func TestScanRejectsZeroTimescale(t *testing.T) {
	dir := t.TempDir()
	opts := fixture.DefaultOptions("zero", fixture.KindNormal)
	s, err := fixture.Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Zero the mvhd timescale field by locating mvhd and patching its payload.
	data, err := readFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	off, size, err := locate(data, "mvhd")
	if err != nil {
		t.Fatal(err)
	}
	if size < 24 {
		t.Fatal("mvhd too small")
	}
	// version(1)+flags(3)+creation(8)+modification(8) = 20; timescale at 20.
	writeU32(data[off+8+20:], 0)
	if err := writeFile(s.Path, data); err != nil {
		t.Fatal(err)
	}
	r, err := Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.Scan()
	f, ok := fault.AsFault(err)
	if !ok || f.Kind != fault.KindCorrupt {
		t.Fatalf("err = %v", err)
	}
	if f.Offset != off {
		t.Fatalf("offset = %d want %d", f.Offset, off)
	}
}
