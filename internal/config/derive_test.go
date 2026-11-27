package config_test

import (
	"testing"

	"mediasync/internal/config"
	"mediasync/internal/model"
	"mediasync/internal/spec"
)

func meta() model.TrackMeta {
	return model.TrackMeta{TrackID: "a", Codec: model.CodecOpus, Channels: 6, SampleRate: 44100}
}

func timeline(nSamples int) *model.TrackTimeline {
	tl := &model.TrackTimeline{TrackID: "a"}
	for i := 0; i < nSamples; i++ {
		tl.Entries = append(tl.Entries, model.TimelineEntry{
			Class: model.EntrySample, SourceIndex: i, ExtSeq: uint64(i),
			Start: model.NS(i) * 20_000_000, Duration: 20_000_000, Bytes: 10,
		})
	}
	return tl
}

func hasCode(d *config.Decision, code model.ErrorKind) bool {
	for _, f := range d.Unsatisfiable {
		if f.Code == code {
			return true
		}
	}
	return false
}

func TestDeriveConversionStepsFromSpec(t *testing.T) {
	m := meta()
	rep := spec.Validate(m, model.TargetSpec{Channels: 2, SampleRate: 48000})
	d := config.Derive(m, timeline(3), rep, model.TargetSpec{MaxBitrate: 96000}, true)
	if !d.Eligible {
		t.Fatalf("should be eligible: %+v", d.Unsatisfiable)
	}
	fields := map[string]bool{}
	for _, s := range d.Steps {
		fields[s.Field] = true
	}
	if !fields[spec.FieldChannels] || !fields[spec.FieldSampleRate] {
		t.Fatalf("steps not driven by diffs: %+v", d.Steps)
	}
	if d.TargetBitrate != 96000 {
		t.Fatalf("bitrate=%d", d.TargetBitrate)
	}
}

func TestDeriveBitrateUnsatisfiable(t *testing.T) {
	m := meta()
	// Target below the Opus floor of 64 kbps.
	d := config.Derive(m, timeline(2), &spec.Report{TrackID: "a"},
		model.TargetSpec{MaxBitrate: 32000}, true)
	if d.Eligible || !hasCode(d, model.ErrBitrateExceeded) {
		t.Fatalf("bitrate conflict not marked: %+v", d)
	}
}

func TestDeriveBudgetUnsatisfiable(t *testing.T) {
	m := meta()
	// 100 samples * 0.4 ms + 2 ms overhead = 42 ms; budget 5 ms.
	d := config.Derive(m, timeline(100), &spec.Report{TrackID: "a"},
		model.TargetSpec{BudgetNS: 5_000_000}, true)
	if d.Eligible || !hasCode(d, model.ErrBudgetExceeded) {
		t.Fatalf("budget conflict not marked: %+v", d)
	}
	if d.EstimatedNS != 42_000_000 {
		t.Fatalf("estimate=%d", d.EstimatedNS)
	}
}

func TestDeriveQualityFloor(t *testing.T) {
	m := meta() // default Opus quality 82
	d := config.Derive(m, timeline(1), &spec.Report{TrackID: "a"},
		model.TargetSpec{MinQuality: 95}, true)
	if d.Quality != 95 {
		t.Fatalf("quality should rise to floor: %d", d.Quality)
	}
	// An unreachable floor (above the hard cap of 100) is reported.
	d = config.Derive(m, timeline(1), &spec.Report{TrackID: "a"},
		model.TargetSpec{MinQuality: 101}, true)
	if !hasCode(d, model.ErrQualityBelowFloor) {
		t.Fatalf("quality floor breach not reported: %+v", d)
	}
}

func TestDeriveBlockedTrackHasNoConfig(t *testing.T) {
	m := meta()
	d := config.Derive(m, timeline(2), &spec.Report{TrackID: "a"}, model.TargetSpec{}, false)
	if d.Eligible || len(d.Steps) != 0 || !hasCode(d, model.ErrDependencyMissing) {
		t.Fatalf("blocked track must have no config: %+v", d)
	}
}

func TestDeriveDeterministic(t *testing.T) {
	m := meta()
	rep := spec.Validate(m, model.TargetSpec{Channels: 2})
	a := config.Derive(m, timeline(7), rep, model.TargetSpec{MaxBitrate: 96000}, true)
	b := config.Derive(m, timeline(7), rep, model.TargetSpec{MaxBitrate: 96000}, true)
	if a.TargetBitrate != b.TargetBitrate || a.EstimatedNS != b.EstimatedNS ||
		a.Quality != b.Quality || len(a.Steps) != len(b.Steps) {
		t.Fatal("non-deterministic derivation")
	}
}
