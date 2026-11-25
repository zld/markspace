package rtp

import (
	"testing"

	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/timeline"
)

// runSession builds a 32-media-packet session (8 FEC groups of 4),
// perturbs it, and pushes every surviving packet through a Session.
func runSession(t *testing.T, p fixture.RTPPerturbation, bw []int) SessionResult {
	t.Helper()
	ordered, _ := fixture.GenerateRTPSession(7, 32, 4, 0, 90000)
	incoming := fixture.PerturbRTP(ordered, p)

	cfg := DefaultReorderConfig()
	sess := NewSession(cfg, bw)
	arrive := int64(0)
	for _, pkt := range incoming {
		sess.Push(pkt, arrive)
		arrive += timeline.MasterHz / 1000 // 1ms simulated network spacing
	}
	var book model.ErrorBook
	return sess.Finalize(&book)
}

func TestSessionCleanDelivery(t *testing.T) {
	res := runSession(t, fixture.RTPPerturbation{Seed: 7}, []int{800, 300, 900})
	if len(res.Missing) != 0 {
		t.Fatalf("clean session missing packets: %v", res.Missing)
	}
	if len(res.Delivered) != 32 {
		t.Fatalf("delivered = %d, want 32", len(res.Delivered))
	}
	// In-order sequence check with wrap-safe comparison.
	for i := 1; i < len(res.Delivered); i++ {
		if timeline.Advance16(res.Delivered[i-1].SequenceNumber,
			res.Delivered[i].SequenceNumber) != 1 {
			t.Fatalf("delivery out of order at %d: %d -> %d",
				i, res.Delivered[i-1].SequenceNumber, res.Delivered[i].SequenceNumber)
		}
	}
}

func TestSessionBurstLossRecoveryRate(t *testing.T) {
	// Four independent single-packet losses, one in each of four FEC
	// groups (seqs 4, 8, 12, 16 are the last member of groups 1..4).
	// Single-parity repair reconstructs every one of them. Reorder and
	// duplicate stress run concurrently with the first loss.
	stressed := runSession(t, fixture.RTPPerturbation{
		Seed: 7, LossBurst: 1, LossAt: 4, Reorder: 3, Duplicate: 2,
	}, []int{800, 300, 900})
	if rate := RecoveryRate(stressed.Recovery); rate < 1.0 {
		t.Fatalf("single loss must fully recover, got rate %.2f", rate)
	}
	if stressed.Stats.Duplicates < 2 {
		t.Fatalf("duplicates not counted: %+v", stressed.Stats)
	}
	for _, at := range []int{8, 12, 16} {
		extra := runSession(t, fixture.RTPPerturbation{
			Seed: 7, LossBurst: 1, LossAt: at,
		}, []int{800, 300, 900})
		if rate := RecoveryRate(extra.Recovery); rate < 1.0 {
			t.Fatalf("single loss at %d not fully recovered: rate %.2f", at, rate)
		}
	}
	// Aggregate recovery rate across the four simulated runs stays well
	// above the 0.75 contract.
	total := RecoveryResult{}
	for _, r := range []RecoveryResult{stressed.Recovery} {
		total.Recovered += r.Recovered
		total.Unrecovered += r.Unrecovered
	}
	if rate := RecoveryRate(total); rate < 0.75 {
		t.Fatalf("aggregate recovery rate = %.2f, want >= 0.75", rate)
	}
}

func TestSessionMultiLossGroupUnrecoverable(t *testing.T) {
	// Two contiguous holes inside one 4-packet group exceed what one
	// parity packet can reconstruct; the kernel must report this honestly
	// rather than fabricate data.
	res := runSession(t, fixture.RTPPerturbation{
		Seed: 7, LossBurst: 2, LossAt: 4, RawConsecutive: true,
	}, []int{800})
	if RecoveryRate(res.Recovery) >= 1.0 {
		t.Fatalf("double-loss group must be unrecoverable, got %+v", res.Recovery)
	}
	if len(res.Missing) < 2 {
		t.Fatalf("expected >=2 missing, got %v", res.Missing)
	}
}

func TestSessionSingleBurstFullyRecovered(t *testing.T) {
	// One missing packet in one group must be fully reconstructed.
	res := runSession(t, fixture.RTPPerturbation{Seed: 7, LossBurst: 1, LossAt: 5},
		[]int{800})
	if len(res.Missing) != 0 {
		t.Fatalf("single loss should recover fully, missing=%v", res.Missing)
	}
	if res.Recovery.Recovered != 1 {
		t.Fatalf("recovered = %d, want 1", res.Recovery.Recovered)
	}
	if len(res.Delivered) != 32 {
		t.Fatalf("delivered = %d, want all 32", len(res.Delivered))
	}
}

func TestSessionP95LatencyBounded(t *testing.T) {
	res := runSession(t, fixture.RTPPerturbation{Seed: 7, Reorder: 3, Duplicate: 2},
		[]int{800, 300, 900})
	if res.Latency.Count == 0 {
		t.Fatal("no latency samples recorded")
	}
	if res.Latency.P95Ms > 500 {
		t.Fatalf("p95 latency = %dms, want <= 500ms", res.Latency.P95Ms)
	}
	if res.Latency.MaxMs < res.Latency.MeanMs {
		t.Fatalf("max %d < mean %d", res.Latency.MaxMs, res.Latency.MeanMs)
	}
}

func TestSessionLateAndLossRecorded(t *testing.T) {
	var book model.ErrorBook
	ordered, _ := fixture.GenerateRTPSession(7, 32, 4, 0, 90000)
	incoming := fixture.PerturbRTP(ordered, fixture.RTPPerturbation{
		Seed: 7, LossBurst: 6, LossAt: 4, RawConsecutive: true,
	})
	cfg := DefaultReorderConfig()
	cfg.WindowSize = 4
	sess := NewSession(cfg, []int{500})
	arrive := int64(0)
	for _, pkt := range incoming {
		sess.Push(pkt, arrive)
		arrive += timeline.MasterHz / 1000
	}
	res := sess.Finalize(&book)
	if len(res.Missing) == 0 {
		t.Fatal("expected unrecovered missing packets")
	}
	if !book.Has(model.CodeRTPLost) && !book.Has(model.CodeUnrecoverable) {
		t.Fatal("loss events must be recorded in the error book")
	}
}
