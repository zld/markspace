package analyze

import (
	"testing"

	"mediamux/media"
)

func mediaTime(v int64) media.Time { return media.NewTime(v, media.Timescale90K) }

func TestComputeQualityDeviationInRange(t *testing.T) {
	// 3 segments of 6s each, 1.2 Mbps media bytes plus small per-segment
	// container overhead, targeting the manifest's declared 1.2 Mbps.
	var samples []QualitySample
	for i := 0; i < 3; i++ {
		samples = append(samples, QualitySample{
			Index: i, Bytes: 901200, DurationMS: 6006,
		})
	}
	sum := ComputeQuality(samples, []int64{1000, 1001}, nil, QualityOptions{TargetBPS: 1200000})
	if sum.ActualBPS <= 0 {
		t.Fatalf("actual=%d", sum.ActualBPS)
	}
	if sum.BitrateDeviationPct < 0 || sum.BitrateDeviationPct > 5 {
		t.Fatalf("deviation %.2f%% outside 0-5%%", sum.BitrateDeviationPct)
	}
	if sum.QualitySamples != 3 || sum.WindowCount != 1 {
		t.Fatalf("samples=%d windows=%d", sum.QualitySamples, sum.WindowCount)
	}
	if sum.KeyframeGapP50MS != 1000 || sum.KeyframeGapP95MS != 1001 {
		t.Fatalf("p50=%d p95=%d", sum.KeyframeGapP50MS, sum.KeyframeGapP95MS)
	}
}

func TestComputeQualityIsDeterministic(t *testing.T) {
	samples := []QualitySample{
		{Index: 0, Bytes: 500000, DurationMS: 6000},
		{Index: 1, Bytes: 700000, DurationMS: 6000},
		{Index: 2, Bytes: 650000, DurationMS: 6000},
		{Index: 3, Bytes: 800000, DurationMS: 6000},
	}
	a := ComputeQuality(samples, []int64{900, 1100, 950}, nil, QualityOptions{TargetBPS: 800000})
	b := ComputeQuality(samples, []int64{900, 1100, 950}, nil, QualityOptions{TargetBPS: 800000})
	if a != b {
		t.Fatalf("non-deterministic summary:\n%+v\n%+v", a, b)
	}
	if a.WindowPeakBPS < a.WindowLowBPS {
		t.Fatalf("peak %d below low %d", a.WindowPeakBPS, a.WindowLowBPS)
	}
}

func TestComputeQualityCountsCorruptionAndTransport(t *testing.T) {
	samples := []QualitySample{
		{Index: 0, Bytes: 1000, DurationMS: 1000},
		{Index: 1, Bytes: 1000, DurationMS: 1000, CorruptMS: 1000},
	}
	raw, err := BuildRTP(10, 1, 0, 100, map[uint16]bool{5: true}, map[uint16]bool{2: true})
	if err != nil {
		t.Fatal(err)
	}
	rtpRes := AnalyzeRTP(raw, RTPOptions{})
	sum := ComputeQuality(samples, nil, rtpRes, QualityOptions{
		TargetBPS: 16000, PacketDurationMS: 33,
	})
	if sum.CorruptSegments != 1 || sum.CorruptDurationMS != 1000 {
		t.Fatalf("corruption accounting: %+v", sum)
	}
	if sum.RTPDuplicates != 1 || sum.RTPMissing != 1 {
		t.Fatalf("rtp accounting: %+v", sum)
	}
	if sum.RTPMissingMS != 33 {
		t.Fatalf("missing ms=%d", sum.RTPMissingMS)
	}
}

func TestSegmentSampleMarksCorruptAsDamagedTime(t *testing.T) {
	good := &SegmentResult{Duration: mediaTime(90000), Bytes: 100}
	bad := &SegmentResult{Duration: mediaTime(45000), Bytes: 50, Corrupt: true}
	if s := SegmentSample(good); s.CorruptMS != 0 || s.DurationMS != 1000 {
		t.Fatalf("good sample=%+v", s)
	}
	if s := SegmentSample(bad); s.CorruptMS != 500 || s.DurationMS != 500 {
		t.Fatalf("bad sample=%+v", s)
	}
}
