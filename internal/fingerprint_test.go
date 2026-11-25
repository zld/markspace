package kernel

import "testing"

func TestSegmentFingerprintStableAndDistinguishesTimeline(t *testing.T) {
	seg := &Segment{
		Index: 1, ID: "segment-001", Time: RangeMS(0, 3000),
		Tracks: []SegmentTrack{{TrackID: "audio", PID: 0x101, Kind: TrackAudio, Samples: []Sample{{PTS90k: 0, Duration90k: 9000, Data: []byte{1, 2}}}}},
	}
	a, err := SegmentFingerprint(seg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SegmentFingerprint(seg)
	if err != nil || a != b {
		t.Fatal("fingerprint not deterministic")
	}
	seg.Time.End90k++
	c, err := SegmentFingerprint(seg)
	if err != nil || c == a {
		t.Fatal("timeline change did not alter fingerprint")
	}
}

func TestFindDuplicateSegments(t *testing.T) {
	mk := func(index int, start uint64) *Segment {
		return &Segment{Index: index, ID: "s", Time: TimeRange{Start90k: start, End90k: start + 10}, Tracks: []SegmentTrack{}}
	}
	// Identical canonical content requires same time fields; build exact copies.
	a := mk(1, 0)
	b := mk(1, 0)
	c := mk(2, 999)
	groups, err := FindDuplicateSegments([]*Segment{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Indexes) != 2 {
		t.Fatalf("groups=%#v", groups)
	}
}
