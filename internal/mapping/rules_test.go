package mapping

import (
	"testing"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

func sampleTable(t *testing.T) *Table {
	t.Helper()
	tb, err := NewTable([]Rule{
		{Source: "a.srt", SourceVersion: "v1", TargetTrack: "timeline-en",
			Language: "en", Offset: clock.Ticks(90000), Enabled: true},
		{Source: "b.vtt", SourceVersion: "v2", TargetTrack: "timeline-en",
			Language: "en", Offset: 0, Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestLookup(t *testing.T) {
	tb := sampleTable(t)
	r, err := tb.Lookup("a.srt", "v1")
	if err != nil || r.TargetTrack != "timeline-en" || int64(r.Offset) != 90000 {
		t.Fatalf("lookup failed: %+v %v", r, err)
	}
	if _, err := tb.Lookup("b.vtt", "v2"); fault.As(err).Kind != fault.KindMissing {
		t.Fatalf("disabled rule must be missing-source, got %v", err)
	}
	if _, err := tb.Lookup("x.srt", "v9"); fault.As(err).Kind != fault.KindMissing {
		t.Fatalf("unknown pair must be missing-source, got %v", err)
	}
}

func TestNewTableRejectsBadAndDuplicateRules(t *testing.T) {
	if _, err := NewTable([]Rule{{Source: "", SourceVersion: "v1", TargetTrack: "t"}}); err == nil {
		t.Fatal("empty source must be rejected")
	}
	if _, err := NewTable([]Rule{
		{Source: "a", SourceVersion: "v1", TargetTrack: "t"},
		{Source: "a", SourceVersion: "v1", TargetTrack: "u"},
	}); err == nil {
		t.Fatal("duplicate source/version must be rejected")
	}
}

func TestApply(t *testing.T) {
	tb := sampleTable(t)
	r, _ := tb.Lookup("a.srt", "v1")
	src := &track.Track{ID: "a.srt", Version: "v1", Format: "srt",
		Cues: []track.Cue{{ID: "a.srt#1", Start: 0, End: 45000, Text: "hi"}}}
	out, err := Apply(r, src)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != "timeline-en" || out.Language != "en" || len(out.Cues) != 1 {
		t.Fatalf("mapped track mismatch: %+v", out)
	}
	if int64(out.Cues[0].Start) != 90000 || int64(out.Cues[0].End) != 135000 ||
		out.Cues[0].Text != "hi" {
		t.Fatalf("offset not applied: %+v", out.Cues[0])
	}
	if _, err := Apply(r, &track.Track{ID: "other", Version: "v1"}); err == nil {
		t.Fatal("rule/track identity mismatch must be rejected")
	}
}

func TestRulesStableOrder(t *testing.T) {
	tb, err := NewTable([]Rule{
		{Source: "z", SourceVersion: "v1", TargetTrack: "t"},
		{Source: "a", SourceVersion: "v1", TargetTrack: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rs := tb.Rules()
	if rs[0].Source != "a" || rs[1].Source != "z" {
		t.Fatalf("rules not in stable sorted order: %v", rs)
	}
}
