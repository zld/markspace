package pipeline

import (
	"math"
	"testing"

	"regional.example/mediad/internal/fixtures"
	"regional.example/mediad/internal/manifest"
)

// runScenario generates fixtures once and processes one scenario.
func runScenario(t *testing.T, sc fixtures.Scenario, dir string) (*Result, *fixtures.Generated) {
	t.Helper()
	gens, err := fixtures.Generate(dir, 41)
	if err != nil {
		t.Fatal(err)
	}
	var g fixtures.Generated
	for _, gg := range gens {
		if gg.Scenario == sc {
			g = gg
		}
	}
	asset, err := manifest.ParseFile(g.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(asset, DefaultOptions())
	if err != nil {
		t.Fatalf("%s: %v", sc, err)
	}
	return res, &g
}

func TestPipelineNormalMeetsContract(t *testing.T) {
	res, _ := runScenario(t, fixtures.ScenarioNormal, t.TempDir())
	if res.ExitCode != 0 {
		t.Fatalf("exit %d, failed metrics: %v", res.ExitCode, res.Quality.Failed())
	}
	if res.SampleRate != 48000 {
		t.Fatalf("rate %d", res.SampleRate)
	}
	if res.Layout != "stereo" {
		t.Fatalf("layout %s", res.Layout)
	}
	if math.Abs(res.Loudness.IntegratedLUFS-(-18)) > 1.5 {
		t.Fatalf("lufs %.2f", res.Loudness.IntegratedLUFS)
	}
	if res.TruePeakDBTP > -1.0 {
		t.Fatalf("true peak %.2f", res.TruePeakDBTP)
	}
	if res.ClippedSamples != 0 {
		t.Fatalf("clipped %d", res.ClippedSamples)
	}
	if res.Manifest.TimelineErrorTicks > 1 {
		t.Fatalf("timeline error %d", res.Manifest.TimelineErrorTicks)
	}
	if res.Manifest.ManifestAlignmentMS > 1 {
		t.Fatalf("alignment %d", res.Manifest.ManifestAlignmentMS)
	}
	if res.Manifest.Packets.DeviationPct > 5 {
		t.Fatalf("bitrate deviation %.2f", res.Manifest.Packets.DeviationPct)
	}
	if len(res.Manifest.OutputSHA256) != 64 {
		t.Fatalf("hash %q", res.Manifest.OutputSHA256)
	}
}

func TestPipelineTruncatedRecovers(t *testing.T) {
	res, _ := runScenario(t, fixtures.ScenarioTruncated, t.TempDir())
	if res.RecoveredSamples <= 0 {
		t.Fatal("no recovered samples")
	}
	if res.MissingDurationMS <= 0 {
		t.Fatal("no missing duration")
	}
	if res.ExitCode == 3 {
		t.Fatal("truncation must be recoverable, not fatal")
	}
	// The truncated tail is a trailing hole rather than a between-interval gap,
	// so it is recorded as missing duration; the interval list never absorbs it.
	if res.Manifest.MissingDurationMS <= 0 {
		t.Fatal("manifest does not record the missing duration")
	}
	for _, iv := range res.Manifest.Intervals {
		if iv.DurationMS == 0 {
			t.Fatal("zero-length interval leaked into the manifest")
		}
	}
}

func TestPipelineCorruptRecovers(t *testing.T) {
	res, _ := runScenario(t, fixtures.ScenarioCorrupt, t.TempDir())
	if res.RecoveredSamples <= 0 {
		t.Fatal("no recovered samples for corruption")
	}
	if res.MissingDurationMS <= 0 {
		t.Fatal("corruption must leave missing duration")
	}
	if res.ExitCode == 3 {
		t.Fatal("corruption must be recoverable")
	}
}

func TestPipelineSparseRebuildsAndDegrades(t *testing.T) {
	res, _ := runScenario(t, fixtures.ScenarioSparse, t.TempDir())
	if res.ExitCode == 3 {
		t.Fatalf("sparse must not be fatal: %v", res.Quality.Failed())
	}
	if res.Manifest.StartTick != 44100 {
		t.Fatalf("start tick %d, non-zero origin lost", res.Manifest.StartTick)
	}
	// The sparse fixture declares a missing optional video track, so the
	// plan must carry a skipped/degraded node rather than fail.
	hasSkipOrDegrade := len(res.Manifest.SkippedTracks) > 0 || len(res.Manifest.DegradedTracks) > 0
	if !hasSkipOrDegrade {
		t.Fatal("degraded/skipped decisions not recorded")
	}
}

func TestPipelineRepeatHashStable(t *testing.T) {
	dir := t.TempDir()
	res1, g := runScenario(t, fixtures.ScenarioNormal, dir)
	asset, err := manifest.ParseFile(g.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := Run(asset, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if res1.OutputSHA256 != res2.OutputSHA256 {
		t.Fatalf("output hash changed: %s vs %s", res1.OutputSHA256, res2.OutputSHA256)
	}
	if res1.PCMHash != res2.PCMHash {
		t.Fatal("pcm hash changed")
	}
}

func TestPipelineHotIsLimited(t *testing.T) {
	res, _ := runScenario(t, fixtures.ScenarioHot, t.TempDir())
	if res.Layout != "stereo" {
		t.Fatalf("layout %s", res.Layout)
	}
	// A clipped master must end below the ceiling after gain and limiting.
	if res.TruePeakDBTP > -1.0 {
		t.Fatalf("hot master true peak %.2f", res.TruePeakDBTP)
	}
	if math.Abs(res.Loudness.IntegratedLUFS-(-18)) > 1.5 {
		t.Fatalf("hot lufs %.2f", res.Loudness.IntegratedLUFS)
	}
}

func TestPipelineSilentDoesNotAmplify(t *testing.T) {
	res, _ := runScenario(t, fixtures.ScenarioSilent, t.TempDir())
	if res.Gain.GainDB != 0 {
		t.Fatalf("silence gained %.2f dB", res.Gain.GainDB)
	}
	if !res.Loudness.Silent {
		t.Fatal("silence not reported")
	}
	if res.ExitCode != 0 {
		t.Fatalf("silent exit %d: %v", res.ExitCode, res.Quality.Failed())
	}
}

func TestPipelineNonZeroStart(t *testing.T) {
	dir := t.TempDir()
	gens, err := fixtures.Generate(dir, 41)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gens {
		if g.Scenario != fixtures.ScenarioSparse {
			continue
		}
		asset, _ := manifest.ParseFile(g.Manifest)
		res, err := Run(asset, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if res.Manifest.StartTick != 44100 {
			t.Fatalf("start tick %d", res.Manifest.StartTick)
		}
		if len(res.Manifest.Intervals) == 0 || res.Manifest.Intervals[0].StartMS < 1000 {
			t.Fatalf("interval start %+v", res.Manifest.Intervals)
		}
	}
}

func TestPipelineDeterministicManifestHash(t *testing.T) {
	dir := t.TempDir()
	gens, _ := fixtures.Generate(dir, 41)
	for _, g := range gens {
		if g.Scenario != fixtures.ScenarioTruncated {
			continue
		}
		asset, _ := manifest.ParseFile(g.Manifest)
		a, err := Run(asset, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		asset2, _ := manifest.ParseFile(g.Manifest)
		b, err := Run(asset2, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if a.OutputSHA256 != b.OutputSHA256 {
			t.Fatalf("truncated recovery hash unstable: %s vs %s", a.OutputSHA256, b.OutputSHA256)
		}
	}
}

func TestPipelineRejectsBadOptions(t *testing.T) {
	opts := DefaultOptions()
	opts.OutputRate = 44100
	if err := opts.Validate(); err == nil {
		t.Fatal("expected output rate rejection")
	}
	opts = DefaultOptions()
	opts.TargetLUFS = 0
	if err := opts.Validate(); err == nil {
		t.Fatal("expected loudness rejection")
	}
}
