package app

import (
	"context"
	"testing"

	"tracecheck/internal/fixture"
	"tracecheck/internal/model"
	"tracecheck/internal/report"
)

// wrapTrace builds a small Opus stream whose 16-bit sequence numbers cross
// the 65535 -> 0 boundary, exercising wrap handling on the real pipeline.
func wrapTrace() *fixture.Trace {
	tr := &fixture.Trace{Profile: "wrap", Seed: 1}
	seqs := []uint16{65533, 65534, 65535, 0, 1, 2}
	var arrival model.Tick
	for i, seq := range seqs {
		tr.Packets = append(tr.Packets, model.PacketRecord{
			Stream:       fixture.OpusStream,
			Codec:        model.CodecOpus,
			Sequence:     seq,
			RTPTimestamp: uint32(i * 960),
			Payload:      []byte{3 << 3, 0xAA, 0xBB}, // 20 ms Opus
			ArrivalTick:  arrival,
			ClockRate:    48000,
		})
		arrival += 20_000
	}
	return tr
}

func TestAnalyzeHandlesSequenceWrap(t *testing.T) {
	cfg := baseConfig(fixture.ProfileClean)
	tr := wrapTrace()
	b := report.NewBuilder("wrap", 1, report.Budget{
		MaxLossPackets:      cfg.LossBudget,
		MaxKeyframeInterval: cfg.KeyframeBudget,
	})
	events, count, dups, anomalies := analyzeRTP(context.Background(), cfg, tr, b)
	if count != 6 {
		t.Fatalf("wrapped stream must recover all 6 packets, got %d", count)
	}
	if dups != 0 || len(anomalies) != 0 {
		t.Fatalf("wrap is not loss or duplication: dups=%d anomalies=%v", dups, anomalies)
	}
	r := b.Build(6, 0, 0, 0, false)
	if model.CountCode(r.Findings, model.ErrSequenceWrap) == 0 {
		t.Fatal("crossing the 16-bit boundary must record a wrap finding")
	}
	// The merged timeline must stay monotonic despite the wrap.
	if !monotonicEvents(events) {
		t.Fatal("timeline regressed after a sequence wrap")
	}
}

func monotonicEvents(events []model.TimelineEvent) bool {
	for i := 1; i < len(events); i++ {
		if events[i].Start < events[i-1].Start {
			return false
		}
	}
	return true
}
