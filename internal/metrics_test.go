package rtpreplay

import (
	"math"
	"testing"
)

func cleanStream(t *testing.T) *StreamState {
	t.Helper()
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	for i := 0; i < 50; i++ {
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, uint16(1+i), uint32(i*960), i == 49, opusPayload(1, 0)),
			ArrivalMs: int64(20 + i*20),
		})
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

func TestMetricsCleanStreamZeroLoss(t *testing.T) {
	ss := cleanStream(t)
	ev := []TimedEvent{{AtMs: 0, Kind: EventBandwidth, ValueBps: 32_000}}
	m, err := ComputeMetrics(ss, ev)
	if err != nil {
		t.Fatal(err)
	}
	if m.LossPackets != 0 || m.DuplicatePackets != 0 || m.BurstLossPackets != 0 {
		t.Fatalf("m=%+v", m)
	}
	if m.Latency.P50Ms != 20 || m.Latency.MaxMs != 20 {
		t.Fatalf("latency=%+v", m.Latency)
	}
	if m.QualityScore != 100 {
		t.Fatalf("quality=%v", m.QualityScore)
	}
	// 2-byte payload every 20ms -> 800 bps actual.
	if m.Bitrate.ActualBps != 800 {
		t.Fatalf("actual=%d", m.Bitrate.ActualBps)
	}
	if math.Abs(m.Bitrate.DeviationPct-(-97.5)) > 0.01 {
		t.Fatalf("dev=%v", m.Bitrate.DeviationPct)
	}
	if m.MediaDurationMs != 1000 {
		t.Fatalf("media=%v", m.MediaDurationMs)
	}
}

func TestMetricsBitrateDeviationUnderOnePercent(t *testing.T) {
	// 1000-byte packets of 60ms media: 600ms of media, 80000 bits total.
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	payload := make([]byte, 1000)
	payload[0] = 3 << 3 // 60 ms TOC, one frame
	for i := 0; i < 10; i++ {
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, uint16(1+i), uint32(i*2880), false, payload),
			ArrivalMs: int64(i * 60),
		})
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ComputeMetrics(ss, []TimedEvent{{AtMs: 0, Kind: EventBandwidth, ValueBps: 133_333}})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.Bitrate.DeviationPct) > 1.0 {
		t.Fatalf("deviation %v exceeds 1%% (actual=%d)", m.Bitrate.DeviationPct, m.Bitrate.ActualBps)
	}
}

func TestMetricsBurstLoss(t *testing.T) {
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	for i := 0; i < 30; i++ {
		if i >= 10 && i < 16 {
			continue // 6 consecutive losses
		}
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, uint16(1+i), uint32(i*960), false, opusPayload(1, 0)),
			ArrivalMs: int64(20 + i*20),
		})
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ComputeMetrics(ss, []TimedEvent{{AtMs: 0, Kind: EventBandwidth, ValueBps: 32_000}})
	if err != nil {
		t.Fatal(err)
	}
	if m.LossPackets != 6 || m.LossPct < 19 || m.LossPct > 21 {
		t.Fatalf("loss=%d pct=%v", m.LossPackets, m.LossPct)
	}
	if len(m.BurstLossRuns) != 1 || m.BurstLossRuns[0].Count != 6 {
		t.Fatalf("runs=%+v", m.BurstLossRuns)
	}
	if m.QualityScore >= 100 || m.QualityScore < 0 {
		t.Fatalf("quality=%v", m.QualityScore)
	}
}

func TestMetricsDuplicateAndReorderCounted(t *testing.T) {
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	seq := []uint16{1, 2, 4, 3, 4, 5, 6}
	for i, s := range seq {
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, s, uint32(int(s-1)*960), false, opusPayload(1, 0)),
			ArrivalMs: int64(20 + i*20),
		})
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ComputeMetrics(ss, []TimedEvent{{AtMs: 0, Kind: EventBandwidth, ValueBps: 32_000}})
	if err != nil {
		t.Fatal(err)
	}
	if m.DuplicatePackets != 1 {
		t.Fatalf("dups=%d", m.DuplicatePackets)
	}
	if m.ReorderedPackets != 1 {
		t.Fatalf("reorder=%d", m.ReorderedPackets)
	}
	if m.LossPackets != 0 {
		t.Fatalf("loss=%d", m.LossPackets)
	}
}

func TestMetricsIntervalsReflectBandwidthChanges(t *testing.T) {
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{Track: "opus", Codec: CodecOpus, ClockHz: 48000}},
	}
	p500 := make([]byte, 500)
	p500[0] = 1 << 3
	p100 := make([]byte, 100)
	p100[0] = 1 << 3
	for i := 0; i < 20; i++ {
		p := p100
		if i < 10 {
			p = p500
		}
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, uint16(1+i), uint32(i*960), false, p),
			ArrivalMs: int64(i * 20),
		})
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ComputeMetrics(ss, []TimedEvent{
		{AtMs: 0, Kind: EventBandwidth, ValueBps: 200_000},
		{AtMs: 200, Kind: EventBandwidth, ValueBps: 40_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Intervals) != 2 {
		t.Fatalf("intervals=%+v", m.Intervals)
	}
	if m.Intervals[0].ActualBps <= m.Intervals[1].ActualBps {
		t.Fatalf("first interval should be faster: %+v", m.Intervals)
	}
	if m.Bitrate.TargetBps != 120_000 {
		t.Fatalf("weighted target=%d", m.Bitrate.TargetBps)
	}
}

func TestMetricsRequiresBandwidthEvents(t *testing.T) {
	ss := cleanStream(t)
	if _, err := ComputeMetrics(ss, nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestPercentileNearestRank(t *testing.T) {
	vals := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(vals, 0.5); got != 5 {
		t.Fatalf("p50=%d", got)
	}
	if got := percentile(vals, 0.9); got != 9 {
		t.Fatalf("p90=%d", got)
	}
	if got := percentile(vals, 0.99); got != 10 {
		t.Fatalf("p99=%d", got)
	}
}
