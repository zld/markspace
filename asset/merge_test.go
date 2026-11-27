package asset

import (
	"bytes"
	"strings"
	"testing"

	"mediatimeline/internal/synthetic"
)

func ver(pk, tid, src, digest string, start, end int64, prio int) Version {
	return Version{
		ProgramKey: pk, TrackID: tid, Source: src, Digest: digest, Priority: prio,
		Interval: Interval{StartNS: start, EndNS: end},
	}
}

func TestMergeGapAndConflict(t *testing.T) {
	m := NewMap()
	m.Register(ver("p", "video", "v1/0.m4s", "aaa", 0, 1_000_000_000, 0))
	// gap [1s,2s): no version
	m.Register(ver("p", "video", "v1/2.m4s", "bbb", 2_000_000_000, 3_000_000_000, 0))
	// conflict on [3s,4s): two digests, v2 has higher priority
	m.Register(ver("p", "video", "v1/3.m4s", "ccc", 3_000_000_000, 4_000_000_000, 0))
	m.Register(ver("p", "video", "v2/3.m4s", "ddd", 3_000_000_000, 4_000_000_000, 5))
	// identical digest duplicates collapse without conflict
	m.Register(ver("p", "video", "v1b/0.m4s", "aaa", 0, 1_000_000_000, 2))

	res := Merge(m)
	var gaps, conflicts int
	for _, is := range res.Issues {
		switch is.Code {
		case IssueGap:
			gaps++
			if is.Interval.StartNS != 1_000_000_000 || is.Interval.EndNS != 2_000_000_000 {
				t.Fatalf("gap span wrong: %+v", is.Interval)
			}
		case IssueConflict:
			conflicts++
		}
	}
	if gaps != 1 || conflicts != 1 {
		t.Fatalf("gaps=%d conflicts=%d issues=%+v", gaps, conflicts, res.Issues)
	}
	// Priority-selected source must be v2.
	for _, mi := range res.Intervals {
		if mi.Interval.StartNS == 3_000_000_000 && mi.Selected.Source != "v2/3.m4s" {
			t.Fatalf("priority rule failed: selected=%s", mi.Selected.Source)
		}
	}
	// Deterministic ordering.
	for i := 1; i < len(res.Intervals); i++ {
		if res.Intervals[i-1].Interval.StartNS > res.Intervals[i].Interval.StartNS {
			t.Fatal("intervals not stably sorted")
		}
	}
}

func TestMergeTrailingGap(t *testing.T) {
	m := NewMap()
	m.Register(ver("p", "audio", "a/0.m4s", "x", 0, 1_000_000_000, 0))
	m.Register(ver("p", "audio", "a/2.m4s", "y", 2_000_000_000, 3_000_000_000, 0))
	res := Merge(m)
	codes := map[string]int{}
	for _, is := range res.Issues {
		codes[is.Code]++
	}
	if codes[IssueGap] != 1 { // only the middle [1s,2s) gap
		t.Fatalf("gap count = %d (%+v)", codes[IssueGap], res.Issues)
	}
}

func TestMergeExplicitTrailingGap(t *testing.T) {
	m := NewMap()
	m.Register(ver("p", "audio", "a/0.m4s", "x", 0, 1_000_000_000, 0))
	// Caller knows the program should span 3 seconds.
	m.Programs["p"]["audio"].Expected = Interval{StartNS: 0, EndNS: 3_000_000_000}
	res := Merge(m)
	var found bool
	for _, is := range res.Issues {
		if is.Code == IssueGap && is.Interval.StartNS == 1_000_000_000 &&
			is.Interval.EndNS == 3_000_000_000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("trailing gap missing: %+v", res.Issues)
	}
}

func synthesizeFragment(t *testing.T) []byte {
	t.Helper()
	return synthetic.BuildVideoFragment(1, 0, 0, []synthetic.VideoSample{
		{Duration: 3000, Width: 16, Height: 8, Seed: 1},
		{Duration: 3000, CTS: 1000, Width: 16, Height: 8, Seed: 2},
		{Duration: 3000, Width: 16, Height: 8, Seed: 3},
	})
}

func TestParseFragmentTruncated(t *testing.T) {
	good := bytes.NewReader(synthesizeFragment(t))
	f, err := ParseFragment(good, 90000)
	if err != nil {
		t.Fatalf("good fragment rejected: %v", err)
	}
	if len(f.Samples) != 3 {
		t.Fatalf("samples = %d", len(f.Samples))
	}
	if f.Samples[0].PTSNS != f.Samples[0].DTSNS {
		t.Fatal("first sample PTS/DTS should match without CTS")
	}
	if f.ContentSHA256 == "" || len(f.ContentSHA256) != 64 {
		t.Fatalf("bad digest: %q", f.ContentSHA256)
	}
	// Truncate the fragment mid-box; parser must fail with a byte offset.
	raw := synthesizeFragment(t)
	cut := len(raw) / 2
	if _, err := ParseFragment(bytes.NewReader(raw[:cut]), 90000); err == nil {
		t.Fatal("truncated fragment unexpectedly accepted")
	} else if !strings.Contains(err.Error(), "byte") {
		t.Fatalf("error missing byte offset: %v", err)
	}
}

func TestScaleNSRejectsBadInput(t *testing.T) {
	if _, err := scaleNS(-1, 90000); err == nil {
		t.Fatal("negative units accepted")
	}
	if _, err := scaleNS(1, 0); err == nil {
		t.Fatal("zero timescale accepted")
	}
	if v, err := scaleNS(90000, 90000); err != nil || v != 1_000_000_000 {
		t.Fatalf("scale = %d err=%v", v, err)
	}
}
