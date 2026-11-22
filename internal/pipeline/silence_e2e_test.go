package pipeline

import (
	"testing"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/media"
	"github.com/archive/mediakernel/internal/rtprecover"
	"github.com/archive/mediakernel/internal/testfixture"
	"github.com/archive/mediakernel/internal/timeline"
)

// zeroRTPPayload zeroes the payload bytes of a marshalled RTP packet while
// keeping its header intact, producing silent PCM after payloadToPCM.
func zeroRTPPayload(raw []byte) []byte {
	out := append([]byte(nil), raw...)
	for i := 12; i < len(out); i++ {
		out[i] = 0
	}
	return out
}

// TestSilenceAppearsEndToEnd proves the full data flow classifies all-zero
// recovered audio as silence on the unified timeline, not as an unknown gap.
func TestSilenceAppearsEndToEnd(t *testing.T) {
	work := t.TempDir()
	mp4Bytes, err := testfixture.GenerateMP4(testfixture.MP4Spec{
		Seed: 7, Timescale: 48000, SampleCount: 120, SampleDelta: 960,
		SampleSize: 64, KeyframeEvery: 2, StartOffset: 4800,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := testfixture.RTPSpec{Seed: 7, ClockRate: 48000, PacketCount: 120, BaseSeq: 0, PayloadSize: 64}
	packets, err := testfixture.GenerateRTP(spec)
	if err != nil {
		t.Fatal(err)
	}
	for i := range packets {
		packets[i] = zeroRTPPayload(packets[i])
	}
	asset := &manifest.Asset{
		ID: "quiet", MP4Path: "m.mp4", RTPPath: "r.rtp",
		Timescale: 48000, ClockRate: 48000, StartOffsetUS: 100_000, TargetBitrate: 2_400_000,
	}
	est, _ := rtprecover.NewEstimator(0.25, 500_000)
	in := &Input{Asset: asset, MP4: mp4Bytes, RTPPackets: packets, Estimator: est}
	cfg := Config{
		WorkDir: work, ReorderWindow: 32, LossThreshold: 8, TargetBitrate: 2_400_000,
		SegmentDurationUS: int64(timeline.Second), Silence: media.DefaultSilenceConfig(),
	}
	result, stats, err := Run(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SilenceMS <= 0 {
		t.Fatalf("expected silence on all-zero audio, got silence_ms=%d intervals=%d",
			stats.SilenceMS, len(result.Silences))
	}
	// All reported intervals over the quiet region must be genuine silence,
	// never gap-covered or partial-window statuses.
	for _, s := range result.Silences {
		if s.Status != manifest.StatusSilent {
			t.Fatalf("quiet region misclassified as %q: %+v", s.Status, s)
		}
	}
}
