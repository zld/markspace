package segment

import (
	"testing"
	"time"

	"rtptrace/internal/manifest"
)

func videoTrack() manifest.Track {
	samples := make([]manifest.Sample, 0, 5)
	for i := uint32(1); i <= 5; i++ {
		samples = append(samples, manifest.Sample{
			Index: i, PresentationTicks: int64(i - 1), DecodeTicks: int64(i-1) * 24000,
			SizeBytes: 100, Sync: i == 1 || i == 4,
		})
	}
	return manifest.Track{
		ID: 1, Kind: manifest.TrackVideo, SourceTimescale: 24000,
		MovieTimescale: 1, Samples: samples,
	}
}

func TestSelectLatestKeyframeNotAfterBoundary(t *testing.T) {
	bp := SelectTrackBoundaries(videoTrack(), 2*time.Second, 3)
	if len(bp.Boundaries) != 3 {
		t.Fatalf("boundaries=%d", len(bp.Boundaries))
	}
	// At 2s, keyframe 1 at 0s is the latest sync not after target.
	if bp.Boundaries[1].DecodeStartSample != 1 || bp.Boundaries[1].State != BoundaryAligned {
		t.Fatalf("boundary1=%+v", bp.Boundaries[1])
	}
	// At 4s, sample 4 is an exact keyframe.
	if bp.Boundaries[2].DecodeStartSample != 4 {
		t.Fatalf("boundary2=%+v", bp.Boundaries[2])
	}
}

func TestMissingKeyframeFailsWithoutMovingTarget(t *testing.T) {
	tr := videoTrack()
	for i := range tr.Samples {
		tr.Samples[i].Sync = false
	}
	bp := SelectTrackBoundaries(tr, 2*time.Second, 2)
	b := bp.Boundaries[1]
	if b.State != BoundaryMissingKey || b.FailureSample != 3 {
		t.Fatalf("boundary=%+v", b)
	}
	if b.PresentationStart != "2" || b.DecodeStartSample != 0 {
		t.Fatalf("target boundary moved: %+v", b)
	}
}

func TestAlignProducesZeroByteAndTimeErrors(t *testing.T) {
	tr := videoTrack()
	m := &manifest.Manifest{
		Duration: 6 * time.Second, SegmentDuration: 2 * time.Second,
		Tracks: []manifest.Track{tr},
		Representations: []manifest.RepresentationTarget{
			{ID: "rep-800k", Bitrate: 800000},
			{ID: "rep-1600k", Bitrate: 1600000},
		},
	}
	ar := Align(m, 2*time.Second, nil)
	if len(ar.Mappings) != 10 {
		t.Fatalf("mappings=%d", len(ar.Mappings))
	}
	if ar.MaxTimeErrorTicks != 0 || ar.TotalByteError != 0 {
		t.Fatalf("errors time=%d byte=%d", ar.MaxTimeErrorTicks, ar.TotalByteError)
	}
	for _, mp := range ar.Mappings {
		if mp.ByteError != 0 || mp.TimeErrorTicks != 0 {
			t.Fatalf("bad mapping: %+v", mp)
		}
		if mp.SegmentID == "" || mp.Representation == "" || mp.InputSample == "" {
			t.Fatalf("incomplete mapping: %+v", mp)
		}
	}
}
