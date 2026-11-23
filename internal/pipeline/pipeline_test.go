package pipeline

import (
	"path/filepath"
	"testing"
)

func generate(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := fixtureGenerate(dir); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}

func TestRunNormalIsBitExactAndAligned(t *testing.T) {
	out := t.TempDir()
	res, err := Run(Options{InputPath: generate(t, "normal.rtp"), OutDir: out,
		SampleRate: 48000, Channels: 2, TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Packets != 250 || res.Stats.Loss != 0 || res.Stats.BadPackets != 0 {
		t.Fatalf("stats = %+v", res.Stats)
	}
	if res.Stats.ManifestAlignmentSample != 0 {
		t.Fatalf("alignment = %d", res.Stats.ManifestAlignmentSample)
	}
	if res.Stats.OutputSHA256 == "" {
		t.Fatal("missing output hash")
	}
	// Five seconds at 48 kHz.
	if res.Stats.OutputSamples != 240000 {
		t.Fatalf("samples = %d", res.Stats.OutputSamples)
	}
	if res.Stats.OutputLUFS < -23.5 || res.Stats.OutputLUFS > -22.5 {
		t.Fatalf("loudness = %f", res.Stats.OutputLUFS)
	}
}

func TestRunRepeatedProducesIdenticalStatsAndHash(t *testing.T) {
	input := generate(t, "normal.rtp")
	first, err := Run(Options{InputPath: input, OutDir: t.TempDir(), TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(Options{InputPath: input, OutDir: t.TempDir(), TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	if first.Stats.OutputSHA256 != second.Stats.OutputSHA256 {
		t.Fatal("hashes differ between identical runs")
	}
	if first.Stats.OutputSamples != second.Stats.OutputSamples ||
		first.Stats.ManifestAlignmentSample != second.Stats.ManifestAlignmentSample {
		t.Fatal("stats differ between identical runs")
	}
}

func TestRunReorderedMatchesNormalOutput(t *testing.T) {
	base, err := Run(Options{InputPath: generate(t, "normal.rtp"), OutDir: t.TempDir(), TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := Run(Options{InputPath: generate(t, "reordered.rtp"), OutDir: t.TempDir(), TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	if reordered.Stats.Duplicates != 3 {
		t.Fatalf("duplicates = %d", reordered.Stats.Duplicates)
	}
	if reordered.Stats.OutputSHA256 != base.Stats.OutputSHA256 {
		t.Fatal("reordered input produced different audio")
	}
}

func TestRunDamagedIsBoundedAndContinues(t *testing.T) {
	out := t.TempDir()
	res, err := Run(Options{InputPath: generate(t, "damaged.rtp"), OutDir: out, TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Loss != 7 {
		t.Fatalf("loss = %d", res.Stats.Loss)
	}
	// bad version + corrupt payload + two truncated records.
	if res.Stats.BadPackets != 4 {
		t.Fatalf("bad = %d", res.Stats.BadPackets)
	}
	if res.Stats.TruncatedPackets != 2 {
		t.Fatalf("truncated = %d", res.Stats.TruncatedPackets)
	}
	// The dropped run plus the corrupt packet leave gaps, but later audio is
	// still present and the alignment must not drift.
	if res.Stats.OutputSamples != 240000 {
		t.Fatalf("samples = %d", res.Stats.OutputSamples)
	}
	if res.Stats.ManifestAlignmentSample != 0 {
		t.Fatalf("alignment = %d", res.Stats.ManifestAlignmentSample)
	}
}

func TestRunWrapCountsLateReplays(t *testing.T) {
	res, err := Run(Options{InputPath: generate(t, "wrap.rtp"), OutDir: t.TempDir(), TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.LateReplays != 3 {
		t.Fatalf("late = %d", res.Stats.LateReplays)
	}
	if res.Stats.OutputSamples != 115200 {
		t.Fatalf("samples = %d", res.Stats.OutputSamples)
	}
}

func TestRunResamplesTo44100(t *testing.T) {
	res, err := Run(Options{InputPath: generate(t, "normal.rtp"), OutDir: t.TempDir(),
		SampleRate: 44100, Channels: 2, TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	// 5 s at 44.1 kHz, within the one-sample rational boundary.
	if d := res.Stats.OutputSamples - 220500; d < -1 || d > 1 {
		t.Fatalf("samples = %d", res.Stats.OutputSamples)
	}
	if res.Stats.ManifestAlignmentSample > 1 {
		t.Fatalf("alignment = %d", res.Stats.ManifestAlignmentSample)
	}
}

func TestRunRejectsBadOptions(t *testing.T) {
	if _, err := Run(Options{InputPath: "x", OutDir: t.TempDir(), SampleRate: 12345}); err == nil {
		t.Fatal("expected sample rate error")
	}
	if _, err := Run(Options{InputPath: "x", OutDir: t.TempDir(), Channels: 6}); err == nil {
		t.Fatal("expected channel error")
	}
	if _, err := Run(Options{InputPath: filepath.Join(t.TempDir(), "missing"), OutDir: t.TempDir()}); err == nil {
		t.Fatal("expected missing input error")
	}
}

func TestRunTimebaseIsIndependentOfOutputRate(t *testing.T) {
	// Asking for 44.1 kHz output keeps the 48 kHz source clock and resamples.
	res, err := Run(Options{InputPath: generate(t, "normal.rtp"), OutDir: t.TempDir(),
		SampleRate: 44100, ClockRate: 48000, TargetLUFS: -23})
	if err != nil {
		t.Fatal(err)
	}
	if d := res.Stats.OutputSamples - 220500; d < -1 || d > 1 {
		t.Fatalf("samples = %d", res.Stats.OutputSamples)
	}
	if res.Stats.BitrateErrorPct > 0.01 || res.Stats.BitrateErrorPct < -0.01 {
		t.Fatalf("bitrate error = %f", res.Stats.BitrateErrorPct)
	}
}
