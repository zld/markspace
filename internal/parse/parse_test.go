package parse_test

import (
	"os"
	"path/filepath"
	"testing"

	"archive/internal/fixtures"
	"archive/internal/model"
	"archive/internal/parse"
)

func genFixtures(t *testing.T) ([]string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	tracks, corrupt, err := fixtures.Generate(dir, 17)
	if err != nil {
		t.Fatalf("generate fixtures: %v", err)
	}
	if len(tracks) != 8 {
		t.Fatalf("expected 8 tracks, got %d", len(tracks))
	}
	return tracks, corrupt
}

func TestParseValidTracks(t *testing.T) {
	tracks, _ := genFixtures(t)
	for _, p := range tracks {
		var tr *parse.Track
		var err error
		switch filepath.Ext(p) {
		case ".mp4":
			tr, err = parse.ParseMP4File(p)
		case ".mxf":
			tr, err = parse.ParseMXFFile(p)
		}
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		if len(tr.Samples) != 3 {
			t.Fatalf("%s: expected 3 samples, got %d", p, len(tr.Samples))
		}
		if tr.TimeScale <= 0 {
			t.Fatalf("%s: invalid timescale %d", p, tr.TimeScale)
		}
		if !tr.HasRTP {
			t.Fatalf("%s: missing RTP sync timestamp", p)
		}
		for _, s := range tr.Samples {
			if s.Offset < 0 || s.Offset+int64(s.Size) > fileSize(t, p) {
				t.Fatalf("%s: sample %d offset %d out of bounds", p, s.Index, s.Offset)
			}
		}
	}
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func requireParseFault(t *testing.T, err error) *model.Fault {
	t.Helper()
	if err == nil {
		t.Fatal("expected a parse fault, got nil")
	}
	f, ok := model.AsFault(err)
	if !ok {
		t.Fatalf("expected *model.Fault, got %T: %v", err, err)
	}
	if f.Stage != model.StageParse {
		t.Fatalf("expected stage=parse, got %s", f.Stage)
	}
	if f.Offset < 0 {
		t.Fatalf("parse fault must preserve a non-negative offset, got %d", f.Offset)
	}
	return f
}

func TestCorruptTruncatedSampleTable(t *testing.T) {
	_, corrupt := genFixtures(t)
	_, err := parse.ParseMP4File(corrupt["truncated_stbl"])
	f := requireParseFault(t, err)
	t.Logf("fault: %v", f)
}

func TestCorruptOutOfRangeOffset(t *testing.T) {
	_, corrupt := genFixtures(t)
	_, err := parse.ParseMP4File(corrupt["oob_offset"])
	f := requireParseFault(t, err)
	t.Logf("fault: %v", f)
}

func TestCorruptMalformedCrop(t *testing.T) {
	_, corrupt := genFixtures(t)
	_, err := parse.ParseMP4File(corrupt["bad_crop"])
	f := requireParseFault(t, err)
	t.Logf("fault: %v", f)
}

func TestCorruptConflictColorParses(t *testing.T) {
	_, corrupt := genFixtures(t)
	tr, err := parse.ParseMXFFile(corrupt["conflict_color"])
	if err != nil {
		t.Fatalf("conflicting colour labels must still parse: %v", err)
	}
	if len(tr.Colors) != 2 {
		t.Fatalf("expected 2 conflicting colour labels, got %d", len(tr.Colors))
	}
}

func TestUnknownBoxStopsTrack(t *testing.T) {
	_, corrupt := genFixtures(t)
	data, err := os.ReadFile(corrupt["oob_offset"])
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the stss box type into an unknown fourcc inside stbl.
	i := indexOf(data, []byte("stss"))
	if i < 0 {
		t.Fatal("stss not found in fixture")
	}
	copy(data[i:], "zzzz")
	_, err = parse.ParseMP4("unknown.mp4", data)
	f := requireParseFault(t, err)
	if int(f.Offset) != i-4 {
		t.Fatalf("fault offset %d, want box start %d", f.Offset, i-4)
	}
}

func TestUnknownKLVKeyStopsTrack(t *testing.T) {
	_, corrupt := genFixtures(t)
	data, err := os.ReadFile(corrupt["conflict_color"])
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 0x07 // break the KLV key prefix
	_, err = parse.ParseMXF("unknown.mxf", data)
	f := requireParseFault(t, err)
	if f.Offset != 0 {
		t.Fatalf("fault offset %d, want 0", f.Offset)
	}
}

func TestTruncatedKLV(t *testing.T) {
	_, corrupt := genFixtures(t)
	data, err := os.ReadFile(corrupt["conflict_color"])
	if err != nil {
		t.Fatal(err)
	}
	_, err = parse.ParseMXF("trunc.mxf", data[:len(data)/2])
	requireParseFault(t, err)
}

func indexOf(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		match := true
		for j := range sub {
			if b[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
