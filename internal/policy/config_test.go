package policy

import (
	"strings"
	"testing"

	"archivecheck/internal/clock"
	"archivecheck/internal/diagnostic"
	"archivecheck/internal/media"
)

func testVideoDescription() media.TrackDescription {
	return media.TrackDescription{
		Index: 2, ID: 1, Kind: "video", Codec: "avc1", Timescale: 30000,
		DurationNS: int64(2 * clock.Second), Width: 1280, Height: 720,
		Channels: 2, ChannelLayout: media.LayoutStereo, SampleRate: 48000,
	}
}

func TestDecideFeasibleConfigIncludesEstimateAndReasons(t *testing.T) {
	cfg, err := Decide(4, testVideoDescription(), Quality{Score: 92}, Constraints{
		BudgetBytes: 1 << 20, QualityFloor: 80, SourceOffset: 77,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Feasible || cfg.EstimatedBytes <= 0 || cfg.EstimatedBytes > 1<<20 {
		t.Fatalf("cfg feasible=%v estimate=%d", cfg.Feasible, cfg.EstimatedBytes)
	}
	if cfg.Selected.Profile == "" || len(cfg.Reasons) < 3 {
		t.Fatalf("incomplete config: %+v", cfg)
	}
	joined := strings.Join(cfg.Reasons, "\n")
	for _, want := range []string{"quality score 92", "budget 1048576", "estimate_bytes="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("reasons missing %q:\n%s", want, joined)
		}
	}
}

func TestDecideBudgetInfeasibleIsStructuredAndNotFakeSuccess(t *testing.T) {
	desc := testVideoDescription()
	desc.DurationNS = int64(30 * clock.Second)
	cfg, err := Decide(5, desc, Quality{Score: 40}, Constraints{BudgetBytes: 1 << 20})
	if err == nil {
		t.Fatal("expected budget infeasibility")
	}
	if cfg.Feasible {
		t.Fatal("returned config must be marked infeasible")
	}
	if !diagnostic.HasCode(err, diagnostic.CodeBudgetInfeasible) {
		t.Fatalf("err = %v, want BUDGET_INFEASIBLE", err)
	}
	de, _ := diagnostic.From(err)
	if de.Phase != diagnostic.PhasePolicy || de.Sample != 5 {
		t.Fatalf("diagnostic location = %+v", de)
	}
	if cfg.EstimatedBytes <= 1<<20 {
		t.Fatalf("unconstrained estimate %d should exceed budget", cfg.EstimatedBytes)
	}
}

func TestDecideExplicitBitrateCapChangesSelection(t *testing.T) {
	cfg, err := Decide(6, testVideoDescription(), Quality{Score: 85}, Constraints{MaxBitrateBps: 700_000})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxBitrateBps != 700_000 {
		t.Fatalf("cap = %d", cfg.MaxBitrateBps)
	}
	if got := cfg.Selected.VideoBitrateBps + cfg.Selected.AudioBitrateBps; got > 700_000 {
		t.Fatalf("selected total %d exceeds cap", got)
	}
}

func TestDecidePropagatesMetadataConflict(t *testing.T) {
	desc := testVideoDescription()
	desc.Channels = 6
	desc.ChannelLayout = media.Layout51
	_, err := Decide(7, desc, Quality{Score: 90}, Constraints{
		Layout: media.RequestedLayout{Channels: 2, ChannelLayout: media.LayoutStereo},
	})
	if !diagnostic.HasCode(err, diagnostic.CodeMetadataConflict) {
		t.Fatalf("err = %v, want metadata conflict", err)
	}
	de, _ := diagnostic.From(err)
	if de.Phase != diagnostic.PhaseLayout {
		t.Fatalf("phase = %s, want layout", de.Phase)
	}
}

func TestDecideDeterministic(t *testing.T) {
	in := Constraints{BudgetBytes: 1 << 20}
	a, err := Decide(8, testVideoDescription(), Quality{Score: 81}, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Decide(8, testVideoDescription(), Quality{Score: 81}, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.EstimatedBytes != b.EstimatedBytes || a.Selected != b.Selected {
		t.Fatalf("decisions differ: %+v vs %+v", a.Selected, b.Selected)
	}
}
