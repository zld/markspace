package media

import (
	"testing"

	"mediadist/timeline"
)

func TestAudioNormalizeChannelLayout(t *testing.T) {
	n, err := NewAudioNormalizer(48000)
	if err != nil {
		t.Fatalf("NewAudioNormalizer: %v", err)
	}
	res, err := n.Normalize(AudioMeta{Source: "edge-a", Channels: 6, SampleRate: 48000})
	if err != nil {
		t.Fatalf("normalize 5.1: %v", err)
	}
	if len(res.Layout) != 6 || res.Layout[3] != "lfe" {
		t.Fatalf("canonical 5.1 layout wrong: %v", res.Layout)
	}
	if res.RateScale.Cmp(timeline.MustNewRat(1, 1)) != 0 {
		t.Fatalf("same-rate scale = %s, want 1/1", res.RateScale)
	}

	bad := []AudioMeta{
		{Source: "x", Channels: 3, SampleRate: 48000},
		{Source: "y", Channels: 2, SampleRate: 48000, Layout: []string{"left", "center"}},
		{Source: "z", Channels: 0, SampleRate: 48000},
	}
	for _, m := range bad {
		if _, err := n.Normalize(m); timeline.CodeOf(err) != "UNSUPPORTED_CHANNEL_LAYOUT" {
			t.Errorf("meta %+v: code = %q, want UNSUPPORTED_CHANNEL_LAYOUT", m, timeline.CodeOf(err))
		}
	}
	if _, err := n.Normalize(AudioMeta{Source: "r", Channels: 2, SampleRate: 11025}); timeline.CodeOf(err) != "INVALID_TIMESCALE" {
		t.Errorf("bad rate code = %q", timeline.CodeOf(err))
	}
}

func TestResampleExactness(t *testing.T) {
	n, _ := NewAudioNormalizer(48000)
	got, err := n.ResampleTicks(8000, 8000)
	if err != nil {
		t.Fatalf("exact 8k->48k: %v", err)
	}
	if got != 48000 {
		t.Fatalf("8000 ticks @8k = %d, want 48000", got)
	}
	// 44100->48000 of a non-multiple tick count cannot be exact.
	if _, err := n.ResampleTicks(100, 44100); timeline.CodeOf(err) != "NON_EXACT_SCALE" {
		t.Fatalf("non-exact conversion code = %q", timeline.CodeOf(err))
	}
	// But one full second always maps exactly.
	got, err = n.ResampleTicks(44100, 44100)
	if err != nil || got != 48000 {
		t.Fatalf("full second conversion: ticks=%d err=%v", got, err)
	}
}

func TestColorMergePriorityAndStats(t *testing.T) {
	merger := NewColorMerger([]string{"origin", "edge-a", "edge-b"})
	rep, err := merger.Merge([]ColorMeta{
		{Source: "origin", Primaries: "bt2020", Transfer: "smpte2084", MatrixCoeffs: "bt2020nc", Range: "tv"},
		{Source: "edge-a", Primaries: "bt2020", Transfer: "bt709", Range: "tv"}, // transfer conflict, matrix missing
		{Source: "edge-b", Primaries: "bt709", Transfer: "smpte2084"},           // primaries conflict, 2 missing
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// Highest-priority "origin" wins every field it signals.
	if rep.Decision.Primaries != "bt2020" || rep.Decision.Transfer != "smpte2084" ||
		rep.Decision.MatrixCoeffs != "bt2020nc" || rep.Decision.Range != "tv" {
		t.Fatalf("priority decision wrong: %+v", rep.Decision)
	}
	if rep.Stats.Conflicts != 2 {
		t.Fatalf("conflicts = %d, want 2 (primaries, transfer)", rep.Stats.Conflicts)
	}
	if rep.Stats.Missing != 3 {
		t.Fatalf("missing = %d, want 3", rep.Stats.Missing)
	}

	// Source values are preserved in the per-field evidence.
	var transfer ColorDecision
	for _, d := range rep.PerField {
		if d.Field == "transfer" {
			transfer = d
		}
	}
	if transfer.Observed["edge-a"] != "bt709" || transfer.Observed["origin"] != "smpte2084" {
		t.Fatalf("losing source values were not preserved: %+v", transfer.Observed)
	}

	// Determinism: merging in a different order yields the same decision.
	rep2, _ := merger.Merge([]ColorMeta{
		{Source: "edge-b", Primaries: "bt709", Transfer: "smpte2084"},
		{Source: "origin", Primaries: "bt2020", Transfer: "smpte2084", MatrixCoeffs: "bt2020nc", Range: "tv"},
		{Source: "edge-a", Primaries: "bt2020", Transfer: "bt709", Range: "tv"},
	})
	if rep2.Decision != rep.Decision {
		t.Fatal("color merge is not order-independent")
	}

	if _, err := merger.Merge([]ColorMeta{{Source: "rogue", Primaries: "bt709"}}); timeline.CodeOf(err) != "UNKNOWN_METADATA_SOURCE" {
		t.Fatalf("unknown source code = %q", timeline.CodeOf(err))
	}
}
