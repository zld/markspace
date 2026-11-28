package container

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/fault"
)

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.mp4")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func twoTrackSpecs() []trackSpec {
	return []trackSpec{
		{
			id: 1, timescale: 1000, duration: 40, handler: "soun", name: "dialogue",
			// VFR: real per-sample durations 10, 30 ticks.
			stts:  [][2]uint32{{1, 10}, {1, 30}},
			stsc:  [][3]uint32{{1, 2, 1}},
			sizes: []uint32{4, 6},
		},
		{
			id: 2, timescale: 1000, duration: 40, handler: "sbtl", name: "subtitle",
			stts:  [][2]uint32{{2, 20}},
			stsc:  [][3]uint32{{1, 2, 1}},
			sizes: []uint32{3, 3},
		},
	}
}

func TestParseValidContainer(t *testing.T) {
	data := buildMP4(twoTrackSpecs())
	f, err := Parse(context.Background(), writeTemp(t, data))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if f.Timescale != 1000 || len(f.Tracks) != 2 {
		t.Fatalf("unexpected file view: %+v", f)
	}
	dialogue := f.Tracks[0]
	if dialogue.Name != "dialogue" || dialogue.Timescale != 1000 {
		t.Fatalf("unexpected track meta: %+v", dialogue)
	}
	if len(dialogue.Samples) != 2 {
		t.Fatalf("want 2 samples, got %d", len(dialogue.Samples))
	}
	// VFR durations must come from stts, never from a fixed frame rate.
	if dialogue.Samples[0].DurationTicks != 10 || dialogue.Samples[1].DurationTicks != 30 {
		t.Fatalf("VFR durations lost: %+v", dialogue.Samples)
	}
	// Second sample offset must equal first sample offset + its size.
	if dialogue.Samples[1].Offset != dialogue.Samples[0].Offset+4 {
		t.Fatalf("sample offsets not contiguous: %+v", dialogue.Samples)
	}
	if dialogue.Samples[0].Offset < f.MdatOffset {
		t.Fatal("sample offset precedes mdat payload")
	}
	if len(dialogue.Marks) != 2 || dialogue.Marks[1].RawTicks != 10 {
		t.Fatalf("timeline marks wrong: %+v", dialogue.Marks)
	}
}

func TestTruncatedContainer(t *testing.T) {
	data := buildMP4(twoTrackSpecs())
	// Cut through the moov box.
	cut := len(data) - 20
	path := writeTemp(t, data[:cut])
	_, err := Parse(context.Background(), path)
	f, ok := fault.AsFault(err)
	if !ok {
		t.Fatalf("want fault, got %v", err)
	}
	if f.Code != fault.ErrTruncated {
		t.Fatalf("want truncated fault, got %v", f)
	}
	if f.Offset < 0 {
		t.Fatalf("truncation must carry an offset, got %d", f.Offset)
	}
}

func TestZeroTimescaleRejected(t *testing.T) {
	specs := twoTrackSpecs()
	specs[0].timescale = 0
	specs[0].duration = 0
	data := buildMP4(specs)
	_, err := Parse(context.Background(), writeTemp(t, data))
	f, ok := fault.AsFault(err)
	if !ok || f.Code != fault.ErrIllegalTimebase {
		t.Fatalf("want illegal_timebase fault, got %v", err)
	}
	if f.Offset <= 0 {
		t.Fatalf("fault must point at mdhd offset, got %d", f.Offset)
	}
}

func TestSampleRangeMismatch(t *testing.T) {
	specs := twoTrackSpecs()
	// stsz declares only one size while stts/stsc expect two.
	specs[0].sizes = []uint32{4}
	data := buildMP4(specs)
	_, err := Parse(context.Background(), writeTemp(t, data))
	f, ok := fault.AsFault(err)
	if !ok || f.Code != fault.ErrSampleRange {
		t.Fatalf("want sample_range fault, got %v", err)
	}
}

func TestOffsetOutsideMdat(t *testing.T) {
	specs := twoTrackSpecs()
	data := buildMP4(specs)
	// Locate the stco entry for the first track's chunk and corrupt it so it
	// points past end of file. stco payload: version(4)+count(4)+first offset.
	idx := indexOfPattern(data, []byte("stco"))
	if idx < 0 {
		t.Fatal("stco box not found")
	}
	offPos := idx + 4 + 8 // skip "stco" + version/flags + entry count
	corrupt := append([]byte(nil), data...)
	for i := 0; i < 4; i++ {
		corrupt[offPos+i] = 0xFF
	}
	_, err := Parse(context.Background(), writeTemp(t, corrupt))
	f, ok := fault.AsFault(err)
	if !ok {
		t.Fatalf("want fault, got %v", err)
	}
	if f.Code != fault.ErrOutOfBounds && f.Code != fault.ErrOverflow {
		t.Fatalf("want out_of_bounds/overflow fault, got %v", f)
	}
}

func TestMissingFtyp(t *testing.T) {
	data := buildMP4NoFtyp(twoTrackSpecs())
	_, err := Parse(context.Background(), writeTemp(t, data))
	f, ok := fault.AsFault(err)
	if !ok || f.Code != fault.ErrContainerDecl {
		t.Fatalf("want container_decl fault, got %v", err)
	}
}

func TestCancelation(t *testing.T) {
	data := buildMP4(twoTrackSpecs())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Parse(ctx, writeTemp(t, data))
	f, ok := fault.AsFault(err)
	if !ok || f.Code != fault.ErrCanceled {
		t.Fatalf("want canceled fault, got %v", err)
	}
}

func indexOfPattern(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
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
