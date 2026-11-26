package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"mediamerge/internal/clock"
	"mediamerge/internal/merge"
	"mediamerge/internal/track"
	"mediamerge/internal/txn"
)

func TestFromMergeAndText(t *testing.T) {
	res := &merge.Result{
		Timelines: map[string]*track.Track{
			"tl": {Cues: []track.Cue{
				{ID: "a#1", Start: clock.Ticks(0), End: clock.Ticks(1)},
				{ID: "a#2", Start: clock.Ticks(2), End: clock.Ticks(3)},
			}},
		},
		Conflicts:  []merge.Conflict{{TargetTrack: "tl"}},
		Gaps:       []track.Gap{{Source: "b.srt", Start: 5, End: 6}},
		Rejected:   []track.Rejected{{Source: "c.srt"}},
		Duplicates: 1,
	}
	m := &Metrics{Case: "conflict"}
	m.FromMerge(res)
	if m.RoundtripOK != 2 || m.RoundtripTotal != 2 || m.Conflicts != 1 ||
		m.Gaps != 1 || m.Rejected != 1 || m.DuplicateCues != 1 {
		t.Fatalf("merge metrics wrong: %+v", m)
	}
	var buf bytes.Buffer
	m.WriteText(&buf)
	out := buf.String()
	for _, want := range []string{"roundtrip=2/2", "conflicts=1", "gap=1", "rejected=1",
		"duplicate_cues=1", "manifest_unchanged=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text output missing %q:\n%s", want, out)
		}
	}
}

func TestFromManifestHashes(t *testing.T) {
	man := &txn.Manifest{
		Version: 1,
		Segments: []txn.Segment{
			{ID: 2, SHA256: strings.Repeat("b", 64)},
			{ID: 1, SHA256: strings.Repeat("a", 64)},
		},
		CueCount: 8,
	}
	m := &Metrics{}
	m.FromManifest(man, 0)
	m.Keyframe(8, 8)
	if m.Segments != 2 || m.HashMismatch != 0 || m.KeyframeHits != 8 {
		t.Fatalf("manifest metrics wrong: %+v", m)
	}
	if m.SegmentHashes[0].ID != 1 {
		t.Fatal("segment hashes must be sorted by id")
	}
	var buf bytes.Buffer
	m.WriteText(&buf)
	out := buf.String()
	if !strings.Contains(out, "segment[1] sha256="+strings.Repeat("a", 64)) {
		t.Fatalf("64-char per-segment hash line missing:\n%s", out)
	}
	if strings.Count(out, "sha256=") != 2 {
		t.Fatalf("want one hash line per segment:\n%s", out)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	m := &Metrics{Case: "normal", RoundtripOK: 8, RoundtripTotal: 8, Segments: 4,
		SegmentHashes: []SegmentHash{{ID: 1, SHA256: strings.Repeat("f", 64)}}}
	var buf bytes.Buffer
	if err := m.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var got Metrics
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("report JSON must be valid: %v", err)
	}
	if got.Case != "normal" || got.Segments != 4 || got.SegmentHashes[0].SHA256 != strings.Repeat("f", 64) {
		t.Fatalf("JSON round trip mismatch: %+v", got)
	}
}
