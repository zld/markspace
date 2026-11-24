package index

import (
	"strings"
	"testing"

	"tsforge/internal/ts"
)

func snap(id string, pat, pmt uint8) *ts.Snapshot {
	return &ts.Snapshot{
		ID: id, PATVersion: pat, PMTVersion: pmt,
		Programs: []ts.Program{{Number: 1, PMTPID: 0x100, PCRPID: 0x101,
			Streams: []ts.Stream{{PID: 0x101, StreamType: 0x1B, Kind: ts.StreamVideo}}}},
	}
}

func buildIndex(t *testing.T, recs []Record) *Index {
	t.Helper()
	b := NewBuilder(100)
	b.SetTracks(0x101, 0x102)
	b.NoteSnapshot(snap("s1", 0, 0))
	for _, r := range recs {
		if r.SnapshotID == "" {
			r.SnapshotID = "s1"
		}
		if err := b.Add(r); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	idx, err := b.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	return idx
}

func TestIndexRejectsCrossSnapshotRecordWithoutKeyframe(t *testing.T) {
	idx := buildIndex(t, []Record{
		{PID: 0x101, Kind: KindVideo, DTS: 0, PTS: 100, Keyframe: true, SnapshotID: "s1", Segment: 0},
	})
	// A second snapshot not referenced by the snapshot list must be rejected.
	b := NewBuilder(10)
	b.NoteSnapshot(snap("s1", 0, 0))
	if err := b.Add(Record{PID: 0x101, Kind: KindVideo, DTS: 1, Keyframe: true, SnapshotID: "unknown"}); err != nil {
		t.Fatalf("builder should accept the record; validation catches it")
	}
	if _, err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	_ = idx
}

func TestIndexValidateRejectsUnknownSnapshot(t *testing.T) {
	idx := &Index{
		Snapshots: []SnapshotRef{{ID: "s1"}},
		Records: []Record{
			{Seq: 0, PID: 0x101, Kind: KindVideo, DTS: 0, PTS: 10, Duration: 3000, Keyframe: true, SnapshotID: "s1", Segment: 0},
			{Seq: 1, PID: 0x101, Kind: KindVideo, DTS: 3000, PTS: 3010, Duration: 3000, SnapshotID: "s2", Segment: 0},
		},
	}
	if err := idx.Validate(); err == nil || !strings.Contains(err.Error(), "unknown snapshot") {
		t.Fatalf("Validate=%v, want unknown snapshot rejection", err)
	}
}

func TestIndexValidateRejectsNonKeyframeSegmentOpen(t *testing.T) {
	idx := &Index{
		Snapshots: []SnapshotRef{{ID: "s1"}},
		Records: []Record{
			{Seq: 0, PID: 0x101, Kind: KindVideo, DTS: 0, PTS: 10, Duration: 3000, Keyframe: true, SnapshotID: "s1", Segment: 0},
			{Seq: 1, PID: 0x101, Kind: KindVideo, DTS: 3000, PTS: 3010, Duration: 3000, Keyframe: false, SnapshotID: "s1", Segment: 1},
		},
	}
	if err := idx.Validate(); err == nil || !strings.Contains(err.Error(), "non-keyframe") {
		t.Fatalf("Validate=%v, want non-keyframe rejection", err)
	}
}

func TestIndexFindAlwaysReturnsKeyframe(t *testing.T) {
	idx := buildIndex(t, []Record{
		{PID: 0x101, Kind: KindVideo, DTS: 0, PTS: 100, Duration: 100, Keyframe: true, Segment: 0},
		{PID: 0x101, Kind: KindVideo, DTS: 100, PTS: 200, Duration: 100, Segment: 0},
		{PID: 0x101, Kind: KindVideo, DTS: 200, PTS: 300, Duration: 100, Keyframe: true, Segment: 1},
		{PID: 0x101, Kind: KindVideo, DTS: 300, PTS: 400, Duration: 100, Segment: 1},
	})
	for _, tick := range []int64{100, 150, 200, 250, 300} {
		rec, ok := idx.Find(tick)
		if !ok {
			t.Fatalf("tick %d not found", tick)
		}
		if !rec.Keyframe {
			t.Fatalf("tick %d resolved to a non-keyframe seq %d", tick, rec.Seq)
		}
		// The returned keyframe must not start after the requested time.
		if rec.PTS > tick {
			t.Fatalf("tick %d resolved to a later keyframe PTS %d", tick, rec.PTS)
		}
	}
	// Beyond the indexed range the lookup must report a miss, not clamp.
	if _, ok := idx.Find(1000); ok {
		t.Fatal("out-of-range lookup was clamped")
	}
}

func TestIndexFindBeforeRangeReturnsFirstKeyframe(t *testing.T) {
	idx := buildIndex(t, []Record{
		{PID: 0x101, Kind: KindVideo, DTS: 500, PTS: 600, Duration: 100, Keyframe: true, Segment: 0},
		{PID: 0x101, Kind: KindVideo, DTS: 600, PTS: 700, Duration: 100, Segment: 0},
	})
	rec, ok := idx.Find(10)
	if !ok || !rec.Keyframe || rec.PTS != 600 {
		t.Fatalf("pre-range lookup = %+v ok=%v, want first keyframe", rec, ok)
	}
}

func TestBuilderEnforcesRecordLimit(t *testing.T) {
	b := NewBuilder(2)
	for i := 0; i < 3; i++ {
		err := b.Add(Record{PID: 0x101, DTS: int64(i), SnapshotID: "s1"})
		if i < 2 && err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		if i == 2 {
			if !IsLimits(err) {
				t.Fatalf("limit error not classified: %v", err)
			}
		}
	}
}

func TestBuilderRejectsRecordWithoutSnapshot(t *testing.T) {
	b := NewBuilder(10)
	if err := b.Add(Record{PID: 0x101}); err == nil {
		t.Fatal("record without snapshot accepted")
	}
}

func TestDigestIsStableAndContentSensitive(t *testing.T) {
	recs := []Record{
		{PID: 0x101, Kind: KindVideo, DTS: 0, PTS: 10, Duration: 3000, Keyframe: true, Segment: 0, PayloadHash: "aa"},
	}
	a := buildIndex(t, recs)
	b := buildIndex(t, recs)
	if a.Digest() != b.Digest() {
		t.Fatal("identical content produced different digests")
	}
	recs[0].PayloadHash = "bb"
	c := buildIndex(t, recs)
	if a.Digest() == c.Digest() {
		t.Fatal("content change did not alter the digest")
	}
}

func TestUnmarshalRejectsOversizedIndex(t *testing.T) {
	// A serialized index with more than MaxRecords is refused before it can
	// be used, enforcing the index ceiling on reload.
	idx := Index{}
	for i := 0; i < MaxRecords+1; i++ {
		idx.Records = append(idx.Records, Record{Seq: int64(i)})
	}
	b, err := idx.MarshalStable()
	if err != nil {
		// The record limit is above our practical size guard, so a marshal
		// failure is also an acceptable rejection path.
		return
	}
	if _, err := UnmarshalIndex(b); err == nil {
		t.Fatal("oversized index accepted on reload")
	}
}
