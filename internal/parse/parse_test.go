package parse

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"mediaforensics/internal/fixture"
	"mediaforensics/internal/model"
)

func parseOK(t *testing.T, data []byte) *Result {
	t.Helper()
	res, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	return res
}

func TestParseIntactContainer(t *testing.T) {
	data, _, err := fixture.GenerateMP4(fixture.DefaultSpec(42, 24))
	if err != nil {
		t.Fatal(err)
	}
	res := parseOK(t, data)
	if len(res.Indexes) != 3 {
		t.Fatalf("want 3 track indexes, got %d", len(res.Indexes))
	}
	video := res.Timeline.Track(1)
	if video == nil || len(video.Samples) != 24 {
		t.Fatalf("video track not built: %+v", video)
	}
	if video.Samples[0].Confidence != model.ConfidenceHigh {
		t.Fatalf("intact samples must be high confidence: %s", video.Samples[0].Confidence)
	}
	if video.Samples[0].Offset < 0 || video.EarliestOffset != video.Samples[0].Offset {
		t.Fatal("earliest source offset must be recorded")
	}
	// Determinism: a second parse yields the same offsets and intervals.
	res2 := parseOK(t, data)
	for i, idx := range res.Indexes {
		for j, off := range idx.SampleOffset {
			if off != res2.Indexes[i].SampleOffset[j] {
				t.Fatalf("offset drift at track %d sample %d", i, j)
			}
		}
	}
}

func TestTruncatedBoxPropagatesOffsetError(t *testing.T) {
	data, layout, err := fixture.GenerateMP4(fixture.DefaultSpec(42, 24))
	if err != nil {
		t.Fatal(err)
	}
	cut, dmg, err := fixture.TruncateBox(data, layout, 128)
	if err != nil {
		t.Fatal(err)
	}
	if dmg.Case != fixture.CaseTruncated {
		t.Fatalf("damage case = %s", dmg.Case)
	}
	res, err := Parse(cut)
	if err == nil {
		t.Fatal("truncated mdat must be a fatal parse error")
	}
	fe := model.AsForensicError(err)
	if fe.Stage != model.StageParseBox || fe.Offset != layout.MdatOffset {
		t.Fatalf("error must carry mdat header offset: got %d want %d",
			fe.Offset, layout.MdatOffset)
	}
	if res == nil || res.Container == nil {
		t.Fatal("partial box-tree evidence must be retained on failure")
	}
}

func TestOutOfRangeIndexReported(t *testing.T) {
	data, layout, err := fixture.GenerateMP4(fixture.DefaultSpec(42, 24))
	if err != nil {
		t.Fatal(err)
	}
	bad, dmg, err := fixture.ShiftChunkOffsets(data, layout, 1)
	if err != nil {
		t.Fatal(err)
	}
	res := parseOK(t, bad)
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Phase, "out_of_range") && e.TrackID == 1 {
			found = true
			if e.Offset < 0 {
				t.Fatal("out-of-range error missing byte offset")
			}
		}
	}
	if !found {
		t.Fatalf("expected stco out-of-range error, got %+v; damage at %d", res.Errors, dmg.Offset)
	}
}

func TestMissingIndexRebuildsIntervals(t *testing.T) {
	spec := fixture.DefaultSpec(42, 24)
	mspec, _ := fixture.MissingIndex(spec, 1)
	data, layout, err := fixture.GenerateMP4(mspec)
	if err != nil {
		t.Fatal(err)
	}
	res := parseOK(t, data)
	var idx *TrackIndex
	for _, i := range res.Indexes {
		if i.TrackID == 1 {
			idx = i
		}
	}
	if idx == nil || !idx.Rebuilt {
		t.Fatal("track 1 must be flagged as rebuilt")
	}
	if len(idx.Intervals) == 0 || idx.Intervals[0].Confidence != model.ConfidenceMedium {
		t.Fatalf("rebuilt interval must be medium confidence: %+v", idx.Intervals)
	}
	// Rebuilt offsets must match the real layout recorded at generation.
	want := layout.Tracks[1]
	for i, off := range idx.SampleOffset {
		if off != want.SampleOffset[i] {
			t.Fatalf("rebuilt offset %d = %d, want %d", i, off, want.SampleOffset[i])
		}
	}
	// Repeated rebuilds are identical.
	res2 := parseOK(t, data)
	if !bytes.Equal(intervalDigest(res), intervalDigest(res2)) {
		t.Fatal("rebuild not reproducible across runs")
	}
}

func TestCorruptSampleExcludedFromTimeline(t *testing.T) {
	data, layout, err := fixture.GenerateMP4(fixture.DefaultSpec(42, 24))
	if err != nil {
		t.Fatal(err)
	}
	bad, dmg, err := fixture.CorruptSample(data, layout, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	res := parseOK(t, bad)
	tr := res.Timeline.Track(1)
	if tr == nil || !tr.Samples[5].Corrupt {
		t.Fatal("corrupted sample must be marked")
	}
	if tr.Samples[5].Offset != dmg.Offset {
		t.Fatalf("corrupt sample offset = %d, want %d", tr.Samples[5].Offset, dmg.Offset)
	}
	valid := tr.ValidSamples()
	for _, s := range valid {
		if s.Index == 5 {
			t.Fatal("corrupt sample must not enter the valid timeline")
		}
	}
}

func TestIllegalLengthAndDepth(t *testing.T) {
	data, _, err := fixture.GenerateMP4(fixture.DefaultSpec(42, 12))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("illegal length", func(t *testing.T) {
		bad := append([]byte(nil), data...)
		bad[0] = 0
		bad[1] = 0
		bad[2] = 0
		bad[3] = 3 // ftyp claims 3 bytes
		if _, err := Parse(bad); err == nil {
			t.Fatal("sub-header box length must be rejected")
		}
	})
	t.Run("size cap", func(t *testing.T) {
		// Walk enforces the cap directly regardless of real byte count.
		_, _, err := Walk(bytes.NewReader(data), MaxInputSize+1)
		if err == nil {
			t.Fatal("input above 256 MiB must be rejected")
		}
	})
}

func TestDepthLimitRejectsDeepNesting(t *testing.T) {
	// Build 18 nested containers; the walk must fail at depth 16.
	var buf []byte
	nest := func(inner []byte, typ string) []byte {
		h := make([]byte, 8)
		binary.BigEndian.PutUint32(h, uint32(8+len(inner)))
		copy(h[4:], typ)
		return append(h, inner...)
	}
	for i := 0; i < 18; i++ {
		buf = nest(buf, "moov")
	}
	r := bytes.NewReader(buf)
	_, _, err := Walk(r, int64(len(buf)))
	if err == nil {
		t.Fatal("nesting past depth 16 must be rejected")
	}
	fe := model.AsForensicError(err)
	if fe.Phase != "depth" {
		t.Fatalf("want depth error, got %s", fe.Phase)
	}
}

func TestMissingRequiredStructure(t *testing.T) {
	data, _, err := fixture.GenerateMP4(fixture.DefaultSpec(42, 12))
	if err != nil {
		t.Fatal(err)
	}
	// Strip the trailing mdat box.
	_, layout, _ := fixture.GenerateMP4(fixture.DefaultSpec(42, 12))
	noMdat := data[:layout.MdatOffset]
	if _, err := Parse(noMdat); err == nil {
		t.Fatal("container without mdat must fail required-structure check")
	}
}

func intervalDigest(res *Result) []byte {
	var b bytes.Buffer
	for _, idx := range res.Indexes {
		for _, iv := range idx.Intervals {
			b.WriteString(iv.Basis)
			b.WriteByte(byte(iv.FirstSample))
			b.WriteByte(byte(iv.LastSample))
		}
		for _, off := range idx.SampleOffset {
			b.WriteByte(byte(off >> 24))
			b.WriteByte(byte(off >> 16))
			b.WriteByte(byte(off >> 8))
			b.WriteByte(byte(off))
		}
	}
	return b.Bytes()
}
