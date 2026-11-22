package verify

import (
	"testing"

	"mediasync/internal/index"
	"mediasync/internal/repair"
)

func frame(track string, i int, corrected int64) *repair.Frame {
	f := &repair.Frame{
		Track:          track,
		Representation: "rep-" + track,
		SegmentNumber:  i + 1,
		SourceOffset:   int64(i * 64),
		CorrectedNS:    corrected,
		RawTimescale:   90000,
	}
	if i%3 == 0 {
		f.Anomalies = append(f.Anomalies, repair.Anomaly{Kind: repair.AnomalyWrap})
	}
	if i > 0 && i%2 == 0 {
		f.Anomalies = append(f.Anomalies,
			repair.Anomaly{Kind: repair.AnomalyVFRShift})
	}
	return f
}

func buildFrames() []*repair.Frame {
	var out []*repair.Frame
	// Video every 33.37ms; audio every 33.33ms -> small, bounded sync error.
	for i := 0; i < 40; i++ {
		out = append(out, frame("video", i, int64(i)*33_366_667))
		out = append(out, frame("audio", i, int64(i)*33_333_333))
	}
	return out
}

func TestComputeMetricsAndRanges(t *testing.T) {
	frames := buildFrames()
	digest := index.Digest{Records: 80, SHA256: "abc"}
	lat := make([]LatencySample, 50)
	for i := range lat {
		lat[i] = LatencySample{Key: "k", LatencyUS: int64(20_000 + i*1000)}
	}
	s := Compute(frames, recoveryReportStub(), lat, digest, true, 1)
	if s.SyncErrorMedianMS > 8 {
		t.Fatalf("median sync = %v, want <=8ms", s.SyncErrorMedianMS)
	}
	if s.SyncErrorP95MS > 20 {
		t.Fatalf("p95 sync = %v, want <=20ms", s.SyncErrorP95MS)
	}
	if s.LatencyP95MS <= 20 || s.LatencyP95MS > 100 {
		t.Fatalf("p95 latency out of band: %v", s.LatencyP95MS)
	}
	if s.TimestampWraps == 0 || s.VFRIntervals < 3 {
		t.Fatalf("anomaly counts wrong: wraps=%d vfr=%d",
			s.TimestampWraps, s.VFRIntervals)
	}
	if s.RecoveryRate < 0.97 || s.IndexRecords != 80 ||
		!s.CheckpointRecovered || s.TruncatedSegments != 1 {
		t.Fatalf("summary fields wrong: %+v", s)
	}
}

func TestComputeRepeatedRunsIdentical(t *testing.T) {
	run := func() Summary {
		frames := buildFrames()
		var lat []LatencySample
		for i := 0; i < 30; i++ {
			lat = append(lat, LatencySample{
				Key: "k", LatencyUS: int64(15_000 + (i*7919)%50_000)})
		}
		return Compute(frames, recoveryReportStub(), lat,
			index.Digest{Records: 80, SHA256: "deadbeef"}, false, 0)
	}
	a, b := run(), run()
	if a != b {
		t.Fatalf("nondeterministic metrics:\n%+v\n%+v", a, b)
	}
}

func TestCheckInvariantsHappyPath(t *testing.T) {
	frames := buildFrames()
	var records []index.Record
	for i, f := range frames {
		records = append(records, index.Record{
			Representation: f.Representation,
			Track:          f.Track,
			CorrectedNS:    f.CorrectedNS,
			ByteOffset:     int64(i * 64),
			InputSize:      1 << 20,
		})
	}
	sizes := map[string]int64{"rep-video": 1 << 20, "rep-audio": 1 << 20}
	if err := CheckInvariants(frames, records, sizes); err != nil {
		t.Fatalf("invariants should hold: %v", err)
	}
}

func TestCheckInvariantsDetectsViolations(t *testing.T) {
	good := frame("video", 0, 100)
	bad := frame("video", 1, 50) // backwards, no discontinuity annotation
	sizes := map[string]int64{"rep-video": 1 << 20}
	if err := CheckInvariants([]*repair.Frame{good, bad}, nil, sizes); err == nil {
		t.Fatal("backwards timestamp without discontinuity must fail")
	}
	// Explicit discontinuity annotation makes the same step legal.
	bad.Anomalies = append(bad.Anomalies,
		repair.Anomaly{Kind: repair.AnomalyDiscontinuity})
	if err := CheckInvariants([]*repair.Frame{good, bad}, nil, sizes); err != nil {
		t.Fatalf("annotated discontinuity must pass: %v", err)
	}
	// Offset outside input range must fail.
	records := []index.Record{{
		Representation: "rep-video", ByteOffset: 1 << 21, InputSize: 1 << 20,
	}}
	if err := CheckInvariants(nil, records, sizes); err == nil {
		t.Fatal("out-of-range offset must fail")
	}
}

func TestPercentileAndMedian(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5}
	if median(v) != 3 || percentile(v, 95) != 5 || percentile(v, 50) != 3 {
		t.Fatal("nearest-rank statistics wrong")
	}
	even := []float64{1, 2, 3, 4}
	if median(even) != 2.5 {
		t.Fatal("even-length median wrong")
	}
	if percentile(nil, 95) != 0 || median(nil) != 0 {
		t.Fatal("empty input must yield zero")
	}
}
