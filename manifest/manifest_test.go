package manifest

import (
	"bytes"
	"testing"

	"mediacore/index"
	"mediacore/media"
	"mediacore/pipeline"
)

// buildResult runs the full scan+pipeline for a synthetic 40 ms timeline and
// returns the index, pipeline result and manifest.
func buildResult(t *testing.T, frames int, gapAt int, gapSize int) (*index.Index, *pipeline.Result, *Manifest) {
	t.Helper()
	all := make([]*media.Frame, 0, frames)
	for i := 0; i < frames; i++ {
		if gapAt >= 0 && i >= gapAt && i < gapAt+gapSize {
			continue
		}
		f := &media.Frame{
			Seq:           uint32(i),
			RTPTimestamp:  uint32(i * 3600),
			CaptureTimeMS: int64(i) * 40,
			Width:         16,
			Height:        16,
			DurationTicks: 3600,
			StreamID:      1,
			Payload:       make([]byte, 256),
		}
		for j := range f.Payload {
			f.Payload[j] = byte(i*13 + j*3)
		}
		all = append(all, f)
	}
	data, err := media.EncodeStream(all)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	cfg := pipeline.DefaultConfig(media.DefaultTimescale)
	res, err := pipeline.Run(idx, pipeline.NewBufferSource(data, media.DefaultLimits()), cfg)
	if err != nil {
		t.Fatal(err)
	}
	timing, err := BindTiming(idx.Entries, res.MediaDurationMS)
	if err != nil {
		t.Fatal(err)
	}
	target := 900
	m, err := Build(idx, res, Options{
		TargetKBPS: target,
		Bandwidth: &BandwidthReportLike{
			EstimateKBPS: 930, InputCeilingKBPS: 950,
			TargetKBPS: target, Reason: "input_ceiling",
		},
		Timing: timing,
	})
	if err != nil {
		t.Fatal(err)
	}
	return idx, res, m
}

func TestManifestRecomputesFromEntries(t *testing.T) {
	_, _, m := buildResult(t, 300, -1, 0)
	if m.Stats.FrameCount != 300 {
		t.Fatalf("frame_count=%d", m.Stats.FrameCount)
	}
	rep := Verify(m, DefaultVerifyOptions())
	if !rep.OK {
		t.Fatalf("verify failed: %+v", rep.Failures)
	}
	if !rep.TimingChecked {
		t.Fatal("timing was not checked")
	}
	if rep.AlignmentMS > 2 {
		t.Fatalf("alignment_error_ms=%d exceeds 2", rep.AlignmentMS)
	}
}

func TestManifestDigestStable(t *testing.T) {
	_, _, a := buildResult(t, 200, -1, 0)
	_, _, b := buildResult(t, 200, -1, 0)
	if a.ManifestDigest != b.ManifestDigest {
		t.Fatalf("digests differ: %s vs %s", a.ManifestDigest, b.ManifestDigest)
	}
}

func TestManifestDetectsTamperedStats(t *testing.T) {
	_, _, m := buildResult(t, 120, -1, 0)
	m.Stats.FrameCount += 1
	rep := Verify(m, DefaultVerifyOptions())
	if rep.OK {
		t.Fatal("tampered frame count must fail verification")
	}
	found := false
	for _, f := range rep.Failures {
		if f.Field == "frame_count" && f.Category == media.CatManifestMismatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing frame_count failure: %+v", rep.Failures)
	}
}

func TestManifestDetectsNonMonotonicTime(t *testing.T) {
	_, _, m := buildResult(t, 120, -1, 0)
	m.Segments[0].EndMS = 1 << 40
	rep := Verify(m, DefaultVerifyOptions())
	if rep.OK {
		t.Fatal("non-monotonic time must fail")
	}
}

func TestManifestGapAffectsStats(t *testing.T) {
	// Drop 50 frames (2 s) in the middle of 300.
	_, res, m := buildResult(t, 300, 120, 50)
	if m.Stats.MissingFrames != 50 {
		t.Fatalf("missing_frames=%d want 50", m.Stats.MissingFrames)
	}
	if len(res.Gaps) == 0 {
		t.Fatal("expected gap refs from burst loss")
	}
	attributed := 0
	for _, s := range res.Segments {
		attributed += s.MissingFrames
	}
	if attributed != 50 {
		t.Fatalf("gap frames attributed to segments=%d want 50", attributed)
	}
	rep := Verify(m, DefaultVerifyOptions())
	if !rep.OK {
		t.Fatalf("verify failed after gap: %+v", rep.Failures)
	}
}

func TestManifestRateLine(t *testing.T) {
	_, _, m := buildResult(t, 300, -1, 0)
	line := m.RateLine()
	if len(line) == 0 || m.Stats.TargetKBPS != 900 {
		t.Fatalf("rate line=%q target=%d", line, m.Stats.TargetKBPS)
	}
}

func TestTimingBindingDetectsDisagreement(t *testing.T) {
	idx, res, _ := buildResult(t, 100, -1, 0)
	// Claim a media duration 500 ms longer than the MP4 samples describe.
	binding, err := BindTiming(idx.Entries, res.DurationMS+500)
	if err != nil {
		t.Fatal(err)
	}
	if binding.AlignmentErrorMS < 400 {
		t.Fatalf("alignment=%d should reflect the 500ms disagreement", binding.AlignmentErrorMS)
	}
	m := &Manifest{Version: ManifestVersion, Timing: binding}
	m.Stats.TargetKBPS = 900
	m.Stats.DurationMS = 0
	m.ManifestDigest = m.ComputeDigest()
	rep := Verify(m, DefaultVerifyOptions())
	if rep.OK {
		t.Fatal("alignment beyond tolerance must fail")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("expected parse error")
	}
}
