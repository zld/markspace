package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/continuity/internal/fingerprint"
	"example.com/continuity/internal/frame"
	"example.com/continuity/internal/segment"
	"example.com/continuity/internal/silence"
)

func readStat(p string) (os.FileInfo, error) { return os.Stat(p) }

func testHeader() Header {
	return Header{SchemaVersion: 1, ConfigVersion: "cfg-v1", InputDigest: "deadbeef"}
}

func testSegment(index int, start, end uint64) *segment.Segment {
	rec := &frame.Record{Version: frame.Version1, Track: "v", Seq: start,
		CaptureNS: int64(start) * 40_000_000, Width: 2, Height: 2, Payload: []byte{1, 2, 3, 4}}
	seg := &segment.Segment{
		Index: index, Track: "v", FrameStart: start, FrameEnd: end,
		StartNS: int64(start) * 40_000_000, EndNS: int64(end) * 40_000_000,
		PayloadBytes: 4,
	}
	seg.Frames = []*frame.Record{rec}
	return seg
}

func TestBuildAndWriteManifests(t *testing.T) {
	h := testHeader()
	segs := []*segment.Segment{testSegment(0, 0, 3), testSegment(1, 3, 5)}
	segRecs, err := BuildSegmentRecords(h, segs)
	if err != nil {
		t.Fatal(err)
	}
	if len(segRecs) != 2 || segRecs[1].RecordChecksum == "" || segRecs[1].InputDigest != "deadbeef" {
		t.Fatalf("seg records wrong: %+v", segRecs)
	}
	// Checksums must be deterministic.
	again, _ := BuildSegmentRecords(h, segs)
	if again[0].RecordChecksum != segRecs[0].RecordChecksum {
		t.Fatal("non-deterministic record checksum")
	}

	rep := &silence.Report{
		Intervals: []silence.Interval{{StartNS: 0, EndNS: 20_000_000, Kind: silence.KindSilence}},
		SilenceNS: 20_000_000,
	}
	silRecs, err := BuildSilenceRecords(h, rep)
	if err != nil {
		t.Fatal(err)
	}
	if silRecs[0].Kind != "silence" || silRecs[0].RecordChecksum == "" {
		t.Fatalf("silence record wrong: %+v", silRecs[0])
	}

	d := fingerprint.NewDeduper()
	r0, _ := d.Apply(segs[0])
	r1, _ := d.Apply(segs[1])
	dedupRecs, err := BuildDedupRecords(h, []*fingerprint.Result{r0, r1})
	if err != nil {
		t.Fatal(err)
	}
	if dedupRecs[0].ConfigVersion != "cfg-v1" || dedupRecs[0].RecordChecksum == "" {
		t.Fatalf("dedup record missing provenance: %+v", dedupRecs[0])
	}

	dir := t.TempDir()
	digests, err := WriteAll(dir, segRecs, silRecs, dedupRecs)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{SegmentsFile, SilenceFile, DedupFile} {
		if digests[name] == "" {
			t.Fatalf("missing digest for %s", name)
		}
		if _, err := readStat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("manifest not written: %v", err)
		}
	}
	// No temp files remain after atomic writes.
	if _, err := readStat(filepath.Join(dir, SegmentsFile+".tmp")); err == nil {
		t.Fatal("temp manifest leaked")
	}
}

func TestSummaryChecksumAndReread(t *testing.T) {
	dir := t.TempDir()
	s := &Summary{
		Header: testHeader(), Frames: 12, Segments: 2, SilenceNS: 1000,
		TargetBitrate: 1000, ActualBitrate: 1005, BitrateErrorPct: 0.5,
		PSNRDB: 45, SSIM: 0.995, ManifestAlignNS: 0,
	}
	data, err := WriteSummary(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if s.SummaryChecksum == "" {
		t.Fatal("summary checksum not set")
	}
	got, data2, err := ReadSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(data2) || got.Frames != 12 || got.SummaryChecksum != s.SummaryChecksum {
		t.Fatal("summary round trip mismatch")
	}
	// Re-writing identical content yields identical bytes.
	data3, err := WriteSummary(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(data3) {
		t.Fatal("summary bytes not deterministic")
	}
}
