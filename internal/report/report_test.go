package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/local/medialab/internal/timeline"
)

func sampleSummary(commit string) Summary {
	return NewBuilder().
		SetCommitID(commit).
		SetAVSync(8).
		SetRecovery(0.875, 7, 1).
		SetLatency(120).
		SetKeyframeGap(500).
		SetConflicts(2).
		SetErrorCount(5).
		SetDamage([]timeline.Window{
			{Start: timeline.MasterHz / 2, End: timeline.MasterHz},
			{Start: 0, End: timeline.MasterHz / 4},
		}).
		MarkHealthy(true).
		Build()
}

func TestSummaryStableJSONAndHash(t *testing.T) {
	a := sampleSummary("commit-1")
	b := sampleSummary("commit-1")
	rawA, err := StableJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	rawB, err := StableJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(rawA) != string(rawB) {
		t.Fatal("stable JSON differs for identical summaries")
	}
	// Ranges must be sorted by start time.
	var decoded Summary
	if err := json.Unmarshal(rawA, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.DamageRanges[0].StartMs != 0 || decoded.DamageRanges[1].StartMs != 500 {
		t.Fatalf("ranges not sorted: %+v", decoded.DamageRanges)
	}
	// Hash ignores the commit id but covers every other field.
	if Hash(a) != Hash(b) {
		t.Fatal("hash unstable across equal content")
	}
	c := sampleSummary("commit-2")
	if Hash(a) != Hash(c) {
		t.Fatal("hash must be commit-id independent")
	}
	c.AVSyncMs = 9
	if Hash(a) == Hash(c) {
		t.Fatal("hash must change when content changes")
	}
}

func TestTextLineContainsRequiredFields(t *testing.T) {
	line := Text(sampleSummary("commit-1"))
	for _, field := range []string{
		"av_sync_ms=8", "recovery_rate=0.8750", "p95_latency_ms=120",
		"keyframe_gap_ms=500", "damage_ranges=[[0,250),[500,1000)]",
		"conflict_count=2", "commit_id=commit-1", "healthy=true",
	} {
		if !strings.Contains(line, field) {
			t.Fatalf("text line %q missing %q", line, field)
		}
	}
}

func TestEmptyDamageRangesRender(t *testing.T) {
	s := NewBuilder().SetCommitID("commit-0").Build()
	line := Text(s)
	if !strings.Contains(line, "damage_ranges=[]") {
		t.Fatalf("empty ranges rendered wrong: %q", line)
	}
	if got := trimFloat(1); got != "1.0000" {
		t.Fatalf("rate format = %q, want 1.0000", got)
	}
	if got := trimFloat(0.75); got != "0.7500" {
		t.Fatalf("rate format = %q, want 0.7500", got)
	}
}

func TestHashDeterministicAcrossBuilders(t *testing.T) {
	build := func() Summary {
		return NewBuilder().SetAVSync(12).SetRecovery(0.9, 9, 1).Build()
	}
	if Hash(build()) != Hash(build()) {
		t.Fatal("separate builders produced different hashes")
	}
}
