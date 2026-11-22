package pipeline

import (
	"testing"

	"streamcheck/damage"
)

func run(t *testing.T, scenario string) *Report {
	t.Helper()
	rep, err := Run(Options{Seed: 42, Scenario: scenario, WatermarkMs: 250})
	if err != nil {
		t.Fatalf("scenario %s: %v", scenario, err)
	}
	return rep
}

func TestEngineNormal(t *testing.T) {
	rep := run(t, "normal")
	if rep.ParseRate() != 1.0 {
		t.Fatalf("parse_rate = %v, want 1.0", rep.ParseRate())
	}
	if rep.RoundtripRate != 1.0 {
		t.Fatalf("roundtrip_rate = %v, want 1.0", rep.RoundtripRate)
	}
	if rep.KeyframeHitRate != 1.0 {
		t.Fatalf("keyframe_hit_rate = %v, want 1.0", rep.KeyframeHitRate)
	}
	if len(rep.Damage) != 0 {
		t.Fatalf("normal stream has damage: %+v", rep.Damage)
	}
	if rep.Packets != genPackets || rep.Loss != 0 || rep.Duplicates != 0 {
		t.Fatalf("packets=%d loss=%d dups=%d", rep.Packets, rep.Loss, rep.Duplicates)
	}
	if len(rep.Segments) == 0 {
		t.Fatal("no timeline segments")
	}
}

func TestEngineFaultScenarios(t *testing.T) {
	rep := run(t, "burstloss")
	if rep.Loss != 9 {
		t.Fatalf("burstloss loss = %d, want 9", rep.Loss)
	}
	if rep.Damage[0].Class != damage.ClassSeqGap {
		t.Fatalf("burstloss damage class = %v", rep.Damage[0].Class)
	}

	rep = run(t, "duplicate")
	if rep.Duplicates != 2 || rep.Packets != genPackets {
		t.Fatalf("duplicate: dups=%d packets=%d", rep.Duplicates, rep.Packets)
	}

	rep = run(t, "reorder")
	if rep.Reordered == 0 || rep.Late != 1 {
		t.Fatalf("reorder: reordered=%d late=%d", rep.Reordered, rep.Late)
	}

	rep = run(t, "truncnal")
	if !rep.HasBoundary {
		t.Fatal("truncnal: no recoverable boundary")
	}
	found := false
	for _, d := range rep.Damage {
		if d.Class == damage.ClassNALTruncated && d.Recoverable() {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncnal: no recoverable truncated damage: %+v", rep.Damage)
	}

	rep = run(t, "corruptlen")
	if rep.ParseErrors == 0 || rep.ParseRate() >= 1.0 {
		t.Fatalf("corruptlen: errors=%d rate=%v", rep.ParseErrors, rep.ParseRate())
	}

	rep = run(t, "noindex")
	if rep.KeyframeHitRate != 0.5 {
		t.Fatalf("noindex hit rate = %v, want 0.5", rep.KeyframeHitRate)
	}
	if len(rep.Damage) < 3 {
		t.Fatalf("noindex damage ranges = %d, want >= 3", len(rep.Damage))
	}
}

func TestEngineDeterministic(t *testing.T) {
	for _, sc := range Scenarios {
		r1 := run(t, sc)
		r2 := run(t, sc)
		if r1.Format() != r2.Format() {
			t.Fatalf("scenario %s: repeated run differs", sc)
		}
	}
}

func TestEngineInvariants(t *testing.T) {
	for _, sc := range Scenarios {
		rep := run(t, sc)
		if damage.Overlaps(rep.Damage) {
			t.Fatalf("scenario %s: overlapping damage ranges", sc)
		}
		for i := 1; i < len(rep.KeyframeTicks); i++ {
			if rep.KeyframeTicks[i] < rep.KeyframeTicks[i-1] {
				t.Fatalf("scenario %s: non-monotonic keyframe ticks", sc)
			}
		}
	}
}

// TestEngineUnaffectedSegmentsStable checks that a burst loss only
// changes the timeline windows covering the lost packets: every other
// segment hash matches the normal stream. It also checks that the
// reorder scenario's late repair restores the full timeline, making its
// segments identical to normal.
func TestEngineUnaffectedSegmentsStable(t *testing.T) {
	normal := run(t, "normal")

	re := run(t, "reorder")
	if len(normal.Segments) != len(re.Segments) {
		t.Fatalf("reorder segment count %d, want %d", len(re.Segments), len(normal.Segments))
	}
	for i := range normal.Segments {
		if normal.Segments[i] != re.Segments[i] {
			t.Fatalf("reorder segment %d differs after complete late repair", i)
		}
	}

	loss := run(t, "burstloss")
	if len(normal.Segments) != len(loss.Segments) {
		t.Fatalf("burstloss segment count %d, want %d", len(loss.Segments), len(normal.Segments))
	}
	diff := 0
	for i := range normal.Segments {
		if normal.Segments[i] != loss.Segments[i] {
			diff++
		}
	}
	// The 9 lost packets span exactly two 22500-tick windows.
	if diff != 2 {
		t.Fatalf("burstloss changed %d segments, want exactly 2 (localised damage)", diff)
	}
}
