package fingerprint

import (
	"testing"

	"mediavault/internal/timeline"
)

func videoSample(order uint64, dts int64, payload []byte) timeline.Sample {
	id := timeline.TrackID{Source: "demo", Index: 0, Kind: timeline.KindVideo}
	return timeline.Sample{
		Track:   id,
		Order:   order,
		Payload: payload,
		TS: timeline.Timestamp{
			Track:     id,
			Timescale: 30000,
			RawDTS:    dts,
			DTS:       timeline.FromTicks(dts, 30000),
			PTS:       timeline.FromTicks(dts, 30000),
			Order:     order,
		},
	}
}

func TestFragmentHashDeterministic(t *testing.T) {
	a := FragmentHash([]byte("hello media"))
	b := FragmentHash([]byte("hello media"))
	c := FragmentHash([]byte("different"))
	if a != b {
		t.Fatal("identical bytes must hash identically")
	}
	if a == c {
		t.Fatal("different bytes must hash differently")
	}
	if len(a) != 64 {
		t.Fatalf("hex sha256 length = %d, want 64", len(a))
	}
}

func TestVideoFrameStatsDeterministic(t *testing.T) {
	payload := deterministic(64)
	s1 := VideoFrame(payload, 0)
	s2 := VideoFrame(payload, 0)
	if s1.FrameHash != s2.FrameHash || s1.Mean != s2.Mean || s1.Variance != s2.Variance {
		t.Fatal("offscreen decode must be deterministic")
	}
	if s1.Width != 16 || s1.Height != 16 {
		t.Fatalf("raster = %dx%d, want 16x16", s1.Width, s1.Height)
	}
	if s1.Mean <= 0 || s1.Variance < 0 {
		t.Fatalf("implausible stats mean=%f var=%f", s1.Mean, s1.Variance)
	}
}

func TestAudioPeakDBFS(t *testing.T) {
	// One near full-scale positive 16-bit sample => ~0 dBFS (32767/32768).
	full := []byte{0xff, 0x7f}
	got := AudioFrame(full)
	if got.PeakDBFS > -0.0001 || got.PeakDBFS < -0.001 {
		t.Fatalf("full-scale peak = %f dBFS, want within 0.001 dB of 0", got.PeakDBFS)
	}
	silent := AudioFrame([]byte{0, 0, 0, 0})
	if !IsSilence(silent) {
		t.Fatal("zero PCM must be reported as silence")
	}
}

func TestOfSampleAlignmentUnderOneMs(t *testing.T) {
	// 1001 ticks @30000 -> 33.366...ms; truncation residual must be < 1ms.
	s := videoSample(1, 1001, deterministic(32))
	fp := OfSample(s)
	if fp.AlignmentNs >= 1_000_000 {
		t.Fatalf("alignment residual %d ns must stay below 1 ms", fp.AlignmentNs)
	}
	if fp.FragmentSHA == "" || fp.FrameSHA == "" {
		t.Fatal("both fragment and frame hashes required")
	}
}

func TestContentDuplicateTimelineDistinct(t *testing.T) {
	payload := deterministic(48)
	// Same bytes at two different DTS positions.
	s1 := videoSample(1, 1001, payload)
	s2 := videoSample(2, 5005, payload)
	seg1 := OfSegment("demo/t0", []timeline.Sample{s1})
	seg2 := OfSegment("demo/t0", []timeline.Sample{s2})

	knownContent := map[string]bool{seg1.ContentKey: true}
	knownTimeline := map[string]bool{seg1.TimelineKey: true}
	MarkDuplicate(&seg2, knownContent, knownTimeline)

	if !seg2.ContentDupe {
		t.Fatal("identical content at a new position must be a content duplicate")
	}
	if !seg2.TimelineDistinct {
		t.Fatal("duplicate content at a different DTS must be timeline-distinct")
	}
	if seg1.SegmentSHA != seg2.SegmentSHA {
		t.Fatal("segment content hash should match for identical bytes")
	}
	if seg1.TimelineKey == seg2.TimelineKey {
		t.Fatal("timeline keys must differ when DTS positions differ")
	}
}

func TestStableKeyReplay(t *testing.T) {
	k1 := StableKey("fingerprint", "digest-x", "p=1")
	k2 := StableKey("fingerprint", "digest-x", "p=1")
	k3 := StableKey("fingerprint", "digest-y", "p=1")
	if k1 != k2 {
		t.Fatal("same input+params must produce a stable key")
	}
	if k1 == k3 {
		t.Fatal("different input digest must change the key")
	}
}

func deterministic(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*7 + 13)
	}
	return out
}
