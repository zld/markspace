package fingerprint

import (
	"path/filepath"
	"testing"

	"mediaregress/internal/fixture"
	"mediaregress/internal/media"
	"mediaregress/internal/split"
	"mediaregress/internal/timeline"
	"mediaregress/internal/track"
)

func setupRaw(t *testing.T, pkts int, payload []byte) (string, *timeline.Timeline, []split.Segment) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "raw.mp4")
	if err := fixture.WriteRawAudioPackets(path, 1000, 100, payload, pkts); err != nil {
		t.Fatal(err)
	}
	in, err := track.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := timeline.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	segs, err := split.Tracks(tl, media.Second)
	if err != nil {
		t.Fatal(err)
	}
	return path, tl, segs
}

func setup(t *testing.T, pkts int, amp func(i int) int16) (string, *timeline.Timeline, []split.Segment) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.mp4")
	if err := fixture.WriteAudioMP4(path, fixture.AudioSpecForTest(pkts, amp)); err != nil {
		t.Fatal(err)
	}
	in, err := track.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := timeline.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	segs, err := split.Tracks(tl, media.Second)
	if err != nil {
		t.Fatal(err)
	}
	return path, tl, segs
}

func TestComputeStable(t *testing.T) {
	path, tl, segs := setup(t, 25, func(i int) int16 { return int16(i) })
	first, errs, err := Compute(path, tl, segs, 100*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	second, _, err := Compute(path, tl, segs, 100*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("lengths %d/%d", len(first), len(second))
	}
	for i := range first {
		if first[i].Digest != second[i].Digest {
			t.Fatalf("digest %d changed: %s vs %s", i, first[i].Digest, second[i].Digest)
		}
		if first[i].Status != StatusComplete || !first[i].Comparable {
			t.Errorf("segment %d status=%s comparable=%v", i, first[i].Status, first[i].Comparable)
		}
	}
}

func TestDistinctContentDistinctDigest(t *testing.T) {
	path, tl, segs := setup(t, 25, func(i int) int16 { return int16(i * 100) })
	res, _, err := Compute(path, tl, segs, 100*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range res {
		if seen[r.Digest] {
			t.Errorf("unexpected duplicate digest for %s", r.SegmentID)
		}
		seen[r.Digest] = true
	}
	if len(Duplicates(res)) != 0 {
		t.Error("duplicates reported for distinct content")
	}
}

func TestLongSegmentDetectedAsDuplicate(t *testing.T) {
	// Two 1 s segments whose sample bytes are byte-identical.
	path, tl, segs := setupRaw(t, 30, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	res, _, err := Compute(path, tl, segs, 100*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	dups := Duplicates(res)
	if len(dups) != 1 {
		t.Fatalf("duplicates=%+v", dups)
	}
	g := dups[0]
	if len(g.Segments) < 2 {
		t.Fatalf("group=%+v", g)
	}
	// All sources are recorded, including their time ranges.
	if len(g.Ranges) != len(g.Segments) {
		t.Errorf("ranges=%v segments=%v", g.Ranges, g.Segments)
	}
	for _, r := range g.Ranges {
		if r.Duration() != media.Second {
			t.Errorf("range=%+v", r)
		}
	}
	for _, r := range res {
		if r.Duplicate && r.DuplicateOf == "" {
			t.Errorf("duplicate without source: %+v", r)
		}
	}
}

func TestShortTailIsIncomplete(t *testing.T) {
	// 12.5 packets cannot happen; use 12 packets of 100 ms = 1.2 s, split with
	// a 1 s target, then a second track segment shorter than the window.
	path, tl, segs := setup(t, 15, func(i int) int16 { return 500 })
	res, _, err := Compute(path, tl, segs, 2*media.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != StatusIncomplete || r.Comparable {
			t.Errorf("segment %s status=%s comparable=%v", r.SegmentID, r.Status, r.Comparable)
		}
	}
	// Identical bytes must not become duplicates, however short.
	if dups := Duplicates(res); len(dups) != 0 {
		t.Fatalf("short segments treated as duplicates: %+v", dups)
	}
}

func TestIncompleteNeverDuplicate(t *testing.T) {
	path, tl, segs := setup(t, 15, func(i int) int16 { return 7 })
	// Window longer than any segment: everything incomplete.
	res, _, err := Compute(path, tl, segs, 3600*media.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) < 2 {
		t.Fatalf("expected multiple segments, got %d", len(res))
	}
	for _, r := range res {
		if r.Comparable || r.Duplicate {
			t.Errorf("incomplete segment %s marked comparable/duplicate", r.SegmentID)
		}
	}
}

func TestDigestIsContentOnly(t *testing.T) {
	// Identical bytes at different timeline positions share a digest; the
	// position travels separately as the source range.
	path, tl, _ := setupRaw(t, 6, []byte{9, 9, 9, 9})
	samples := tl.Tracks[0].Samples
	a := split.Segment{ID: "a0/0000", Track: "a0", StartSample: 0, EndSample: 2,
		Window: media.Window{Start: 0, End: 200 * media.Millisecond}}
	b := a
	b.ID = "a0/0001"
	b.Window = media.Window{Start: 200 * media.Millisecond, End: 400 * media.Millisecond}
	b.StartSample, b.EndSample = 2, 4
	if len(samples) < 4 {
		t.Fatalf("need 4 samples, have %d", len(samples))
	}
	res, _, err := Compute(path, tl, []split.Segment{a, b}, 100*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Digest != res[1].Digest {
		t.Fatal("identical content must share a digest")
	}
	if res[0].Window.Start == res[1].Window.Start {
		t.Fatal("source ranges must remain distinct")
	}
}

func TestSameContentAcrossTracksGroupedOnce(t *testing.T) {
	path, tl, segs := setupRaw(t, 30, []byte{4, 4, 4, 4})
	res, _, err := Compute(path, tl, segs, 100*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	dups := Duplicates(res)
	if len(dups) == 0 {
		t.Fatal("identical output segments were not grouped")
	}
	// One group carries every source range; only the primary is a result.
	g := dups[0]
	if g.Primary == "" || len(g.Segments) < 2 {
		t.Fatalf("group=%+v", g)
	}
	var primaries int
	for _, r := range res {
		if r.Digest == g.Digest && !r.Duplicate {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("primary count=%d", primaries)
	}
}

func TestUnreadableTruncatedSegment(t *testing.T) {
	path, tl, segs := setup(t, 30, func(i int) int16 { return 1 })
	// Mark the timeline as truncated mid-way; segments past that point become
	// unreadable rather than silently hashed.
	tl.Tracks[0].Truncated = true
	tl.Tracks[0].TruncatedAt = 1500 * media.Millisecond
	tl.Tracks[0].TruncatedOffset = 999
	res, errs, err := Compute(path, tl, segs, 100*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res {
		if r.Window.Start >= 1500*media.Millisecond {
			found = true
			if r.Status != StatusUnreadable || r.Comparable {
				t.Errorf("segment %s status=%s", r.SegmentID, r.Status)
			}
		}
	}
	if !found {
		t.Fatal("no segment past the truncation point")
	}
	if len(errs) == 0 {
		t.Fatal("no truncation error")
	}
}

func TestMissingTimelineTrack(t *testing.T) {
	path, tl, _ := setup(t, 10, func(i int) int16 { return 0 })
	segs := []split.Segment{{ID: "x0/0000", Track: "x0", Window: media.Window{Start: 0, End: media.Second}}}
	res, errs, err := Compute(path, tl, segs, 100*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 || res[0].Status != StatusUnreadable {
		t.Fatalf("res=%+v errs=%v", res, errs)
	}
}

func TestComputeRejectsBadWindow(t *testing.T) {
	path, tl, segs := setup(t, 5, func(i int) int16 { return 0 })
	_, _, err := Compute(path, tl, segs, 0)
	if !media.IsClass(err, media.ClassInvalidTimebase) {
		t.Fatalf("err=%v", err)
	}
}

func TestAlgorithmVersioned(t *testing.T) {
	if Algorithm == "" {
		t.Fatal("algorithm must be labelled")
	}
}
