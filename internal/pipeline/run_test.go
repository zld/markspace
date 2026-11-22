package pipeline

import (
	"testing"

	"example.com/mediaforge/internal/merrors"
	"example.com/mediaforge/internal/rtp"
	"example.com/mediaforge/internal/segment"
)

func runDefault(t *testing.T) (*Scenario, *Report) {
	t.Helper()
	sc, err := Generate(DefaultScenarioConfig())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Process(sc.Input)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	return sc, rep
}

func TestScenarioMeetsDemoInvariants(t *testing.T) {
	sc, rep := runDefault(t)

	if rep.RoundtripRate < 0.99 || rep.RoundtripRate > 1.0 {
		t.Fatalf("roundtrip_rate = %v, want 0.99..1.00", rep.RoundtripRate)
	}
	if rep.KeyframeHitRate < 0.95 || rep.KeyframeHitRate > 1.0 {
		t.Fatalf("keyframe_hit_rate = %v, want 0.95..1.00", rep.KeyframeHitRate)
	}
	if rep.SegmentCount != sc.Config.Segments {
		t.Fatalf("segments = %d, want %d", rep.SegmentCount, sc.Config.Segments)
	}
	if len(rep.SegmentHashes) != rep.SegmentCount {
		t.Fatalf("hashes = %d, segments = %d", len(rep.SegmentHashes), rep.SegmentCount)
	}
	for _, h := range rep.SegmentHashes {
		var zero [32]byte
		if h.SHA256 == zero {
			t.Fatalf("segment %d has empty hash", h.Index)
		}
	}
	if rep.Recovered != 1 {
		t.Fatalf("recovered_packets = %d, want 1", rep.Recovered)
	}
	if rep.UnrecoveredGaps < 1 {
		t.Fatalf("unrecovered_gaps = %d, want >= 1", rep.UnrecoveredGaps)
	}
	if rep.Late < 1 {
		t.Fatalf("late_packets = %d, want >= 1", rep.Late)
	}
	if rep.ClockWraps < 1 {
		t.Fatalf("clock_wraps = %d, want >= 1", rep.ClockWraps)
	}
	if rep.DriftPPM < 100 || rep.DriftPPM > 140 {
		t.Fatalf("drift_ppm_est = %d, want 100..140", rep.DriftPPM)
	}
	if rep.Truncated != 1 {
		t.Fatalf("truncated_errors = %d, want 1", rep.Truncated)
	}
	if rep.OutOfSync != 0 {
		t.Fatalf("out-of-sync samples = %d, want 0", rep.OutOfSync)
	}
	if rep.RemuxBytes == 0 {
		t.Fatal("remux produced no bytes")
	}
}

func TestHashesAreReproducible(t *testing.T) {
	_, first := runDefault(t)
	_, second := runDefault(t)
	if len(first.SegmentHashes) != len(second.SegmentHashes) {
		t.Fatalf("hash counts differ: %d vs %d", len(first.SegmentHashes), len(second.SegmentHashes))
	}
	if i, ok := segment.Equal(first.SegmentHashes, second.SegmentHashes); !ok {
		t.Fatalf("segment %d hash differs between runs", i)
	}
	if first.RoundtripRate != second.RoundtripRate || first.Recovered != second.Recovered {
		t.Fatal("report summary is not reproducible")
	}
}

func TestScenarioIsDeterministic(t *testing.T) {
	a, err := Generate(DefaultScenarioConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(DefaultScenarioConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Input.MP4) != len(b.Input.MP4) {
		t.Fatalf("mp4 lengths differ: %d vs %d", len(a.Input.MP4), len(b.Input.MP4))
	}
	for i := range a.Input.MP4 {
		if a.Input.MP4[i] != b.Input.MP4[i] {
			t.Fatalf("mp4 differs at byte %d", i)
		}
	}
	if len(a.Input.SenderReports) != len(b.Input.SenderReports) {
		t.Fatal("sender report count differs")
	}
	for i := range a.Input.SenderReports {
		if a.Input.SenderReports[i] != b.Input.SenderReports[i] {
			t.Fatalf("sender report %d differs", i)
		}
	}
}

func TestRecoveredSampleIsDeliveredIntact(t *testing.T) {
	_, rep := runDefault(t)
	if rep.Recovered != 1 {
		t.Fatalf("recovered = %d", rep.Recovered)
	}
	// Roundtrip can only stay near 1.0 if the repaired payload is byte-exact.
	if rep.DeliveredSamples == 0 {
		t.Fatal("no samples delivered")
	}
	if rep.RoundtripRate < 0.99 {
		t.Fatalf("recovered payload was not byte-exact: rate %v", rep.RoundtripRate)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	payload := []byte("media-unit")
	f, err := Frame(0xdeadbeef, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != FrameSize {
		t.Fatalf("frame = %d bytes", len(f))
	}
	ts, got, err := Unframe(f)
	if err != nil {
		t.Fatal(err)
	}
	if ts != 0xdeadbeef || string(got) != string(payload) {
		t.Fatalf("round trip = %x %q", ts, got)
	}
	if _, err := Frame(0, make([]byte, FrameSize)); !merrors.IsCategory(err, merrors.Bounds) {
		t.Fatalf("oversize frame err = %v", err)
	}
	if _, _, err := Unframe(make([]byte, FrameSize-1)); !merrors.IsCategory(err, merrors.Malformed) {
		t.Fatalf("short frame err = %v", err)
	}
}

func TestPlaceRepairedAcrossWrap(t *testing.T) {
	present := map[uint16]rtp.Packet{99: {Seq: 99}, 101: {Seq: 101}}
	// Use a mid-range position to keep the raw timestamp in uint32 range.
	ext := map[uint16]uint64{99: 1_000_000, 101: 1_003_000}
	got, err := placeRepaired(present, 100, 1_001_500, ext)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1_001_500 {
		t.Fatalf("placed = %d, want 1001500", got)
	}
	// Across the 32-bit wrap: the raw timestamp is small (just past the
	// wrap) while the extended neighbours are large; sign extension must
	// recover the large position.
	wrapExt := map[uint16]uint64{99: 4294968000, 101: 4294971000}
	got, err = placeRepaired(present, 100, 1000, wrapExt)
	if err != nil {
		t.Fatal(err)
	}
	// Raw 1000 is 296 ticks past neighbour 99's raw 4294967704-equivalent,
	// i.e. 4294968000 + 296 after sign extension.
	if got != 4294968296 {
		t.Fatalf("wrap placement = %d, want 4294968296", got)
	}
	if _, err := placeRepaired(map[uint16]rtp.Packet{}, 5, 1, nil); !merrors.IsCategory(err, merrors.Empty) {
		t.Fatalf("isolated err = %v", err)
	}
}

func TestProcessRejectsBadInput(t *testing.T) {
	sc, err := Generate(DefaultScenarioConfig())
	if err != nil {
		t.Fatal(err)
	}
	sc.Input.MP4 = []byte("not an mp4")
	if _, err := Process(sc.Input); err == nil {
		t.Fatal("expected error for malformed mp4")
	}
}

func TestGenerateValidation(t *testing.T) {
	cases := []ScenarioConfig{
		{VideoPackets: 4, AudioPackets: 8, Segments: 4},
		{VideoPackets: 160, AudioPackets: 4, Segments: 4},
		{VideoPackets: 160, AudioPackets: 300, Segments: 0},
		{VideoPackets: 160, AudioPackets: 300, Segments: 4, Loss: -1},
		{VideoPackets: 160, AudioPackets: 300, Segments: 4, DriftPPM: 1_000_000},
	}
	for i, cfg := range cases {
		if _, err := Generate(cfg); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestReportErrorListIsBounded(t *testing.T) {
	rep := &Report{}
	for i := 0; i < MaxRecordedErrors+50; i++ {
		rep.Record(merrors.New(merrors.StageJitter, merrors.Gap, "gap %d", i))
	}
	if len(rep.Errors) != MaxRecordedErrors {
		t.Fatalf("errors = %d, want %d", len(rep.Errors), MaxRecordedErrors)
	}
}

// BenchmarkProcess measures end-to-end packet throughput on the generated
// scenario, which the project targets at >= 50k packets/second.
func BenchmarkProcess(b *testing.B) {
	sc, err := Generate(DefaultScenarioConfig())
	if err != nil {
		b.Fatal(err)
	}
	total := 0
	for _, tr := range sc.Input.Tracks {
		total += len(tr.Packets)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Process(sc.Input); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(total*b.N)/b.Elapsed().Seconds(), "packets/s")
}
