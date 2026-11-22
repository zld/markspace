package pipeline

import (
	"testing"

	"github.com/pairwise/mediad/internal/fixtures"
)

// TestDuplicateFeedsCollapseOnIdenticalPayload proves the end-to-end dedupe
// path: two files whose video samples are byte-identical must produce one
// fingerprint, while a third file with different payloads stays distinct.
func TestDuplicateFeedsCollapseOnIdenticalPayload(t *testing.T) {
	dir := t.TempDir()
	spec := func(seed uint64) fixtures.FileSpec {
		return fixtures.FileSpec{Timescale: 1000, Brands: []string{"isom"},
			BreakStszAt: -1, BreakSizeOf: -1,
			Tracks: []fixtures.TrackSpec{{
				ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
				Samples: 120, KeyEvery: 12, SampleSize: 64, SampleSeed: seed,
			}}}
	}
	a := writeFile(t, dir, "a.mp4", spec(3))
	b := writeFile(t, dir, "b.mp4", spec(3)) // same seed -> same bytes
	c := writeFile(t, dir, "c.mp4", spec(4))

	var all []Segment
	for _, path := range []string{a, b, c} {
		sess, err := OpenSession(path)
		if err != nil {
			t.Fatal(err)
		}
		planner, err := sess.Planner()
		if err != nil {
			sess.Close()
			t.Fatal(err)
		}
		res, err := planner.Run(sess.ChapterRequests(3))
		if err != nil {
			sess.Close()
			t.Fatal(err)
		}
		all = append(all, res.Segments...)
		sess.Close()
	}
	if len(all) != 9 {
		t.Fatalf("segments=%d want 9 (three chapters per file)", len(all))
	}
	// Count repeated payloads across files. Content identity is the same
	// evidence the batch dedupe uses; this asserts the fixture really does
	// produce byte-identical camera feeds.
	seen := map[string]string{}
	var duplicates int
	for i := range all {
		key := all[i].SHA256
		if _, ok := seen[key]; ok {
			duplicates++
			continue
		}
		seen[key] = all[i].ID
	}
	if duplicates != 3 {
		t.Fatalf("duplicates=%d want 3 (file b mirrors file a)", duplicates)
	}
}

func TestSilenceUnknownNeverBecomesSilentOnMissingTrack(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "video_only.mp4", fixtures.FileSpec{
		Timescale: 1000, Brands: []string{"isom"}, BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []fixtures.TrackSpec{{
			ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
			Samples: 60, KeyEvery: 6, SampleSize: 32, SampleSeed: 5,
		}},
	})
	sess, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if len(sess.Silence) != 0 {
		t.Fatalf("video-only file reported audio results: %+v", sess.Silence)
	}
	planner, _ := sess.Planner()
	res, err := planner.Run(sess.Requests())
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range res.Segments {
		if seg.SilenceKnown || seg.SilentNS != 0 {
			t.Fatalf("missing audio track produced a silence claim: %+v", seg)
		}
	}
}

func TestAACSoundtrackSilenceIsDetectedEndToEnd(t *testing.T) {
	dir := t.TempDir()
	// 1 s of audible frames, 0.5 s of zero-payload frames, 1 s audible.
	silence := []fixtures.Range{{Start: 48000, End: 72000}}
	path := writeFile(t, dir, "aac.mp4", fixtures.FileSpec{
		Timescale: 1000, Brands: []string{"isom"}, BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []fixtures.TrackSpec{
			{ID: 1, Codec: fixtures.CodecAVC, Timescale: 90000, SampleDur: 3000,
				Samples: 150, KeyEvery: 15, SampleSize: 32, SampleSeed: 6},
			{ID: 2, Codec: fixtures.CodecAAC, Timescale: 48000, SampleDur: 1024,
				Samples: 150, KeyEvery: 1, SampleSize: 64, Silence: silence, SampleSeed: 7},
		},
	})
	sess, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, ok := sess.Silence[2]
	if !ok || !res.Detectable {
		t.Fatalf("AAC silence not analysed: %+v", res)
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(res.Silence) != 1 {
		t.Fatalf("silence windows=%d want 1: %+v", len(res.Silence), res.Silence)
	}
	planner, _ := sess.Planner()
	out, err := planner.Run(sess.Requests())
	if err != nil {
		t.Fatal(err)
	}
	var known bool
	for _, seg := range out.Segments {
		if seg.SilenceKnown && seg.SilentNS > 0 {
			known = true
		}
	}
	if !known {
		t.Fatal("no segment reported AAC-derived silence")
	}
}
