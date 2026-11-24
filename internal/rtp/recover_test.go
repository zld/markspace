package rtp

import (
	"testing"

	"github.com/local/medialab/internal/fixture"
	"github.com/local/medialab/internal/model"
	"github.com/pion/rtp"
)

func TestRecoverSingleLossPerGroup(t *testing.T) {
	pkts, manifest := fixture.GenerateRTPSession(7, 8, 4, 0, 90000)
	rec := NewRecoverer()
	groupOf := buildGroups(t, rec, pkts)

	// Lose exactly one media packet (seq 2) in the first group.
	missing := []uint16{2}
	var book model.ErrorBook
	out, res := rec.Recover(&book, missing, groupOf)
	if res.Recovered != 1 || res.Unrecovered != 0 {
		t.Fatalf("recovery = %+v, want 1 recovered", res)
	}
	rebuilt, ok := out[2]
	if !ok {
		t.Fatal("seq 2 not reconstructed")
	}
	// The rebuilt payload must equal the original generated payload.
	orig := pkts[2].Payload
	if len(rebuilt) != len(orig) {
		t.Fatalf("rebuilt len %d, want %d", len(rebuilt), len(orig))
	}
	for i := range orig {
		if rebuilt[i] != orig[i] {
			t.Fatalf("rebuilt payload mismatch at byte %d", i)
		}
	}
	if book.Has(model.CodeUnrecoverable) {
		t.Fatal("single-loss group must be recoverable")
	}
	_ = manifest
}

func TestRecoverMultipleLossUnrecoverable(t *testing.T) {
	pkts, _ := fixture.GenerateRTPSession(7, 8, 4, 0, 90000)
	rec := NewRecoverer()
	groupOf := buildGroups(t, rec, pkts)

	// Two holes in the same 4-packet group cannot be solved by one parity.
	missing := []uint16{0, 1}
	var book model.ErrorBook
	out, res := rec.Recover(&book, missing, groupOf)
	if res.Unrecovered != 2 || res.Recovered != 0 {
		t.Fatalf("recovery = %+v, want 2 unrecovered", res)
	}
	if len(out) != 0 {
		t.Fatal("no payload may be fabricated for a double loss")
	}
	if !book.Has(model.CodeUnrecoverable) {
		t.Fatal("double loss must raise E_UNRECOVERABLE")
	}
}

func TestRecoveryRate(t *testing.T) {
	if got := RecoveryRate(RecoveryResult{Recovered: 3, Unrecovered: 1}); got != 0.75 {
		t.Fatalf("rate = %v, want 0.75", got)
	}
	if got := RecoveryRate(RecoveryResult{}); got != 1 {
		t.Fatalf("empty recovery rate = %v, want 1", got)
	}
}

// buildGroups registers every group in the recoverer and returns the
// sequence -> base timestamp lookup used during reconstruction.
func buildGroups(t *testing.T, rec *Recoverer, pkts []*rtp.Packet) func(seq uint16) (uint32, bool) {
	t.Helper()
	const groupSize = 4
	groupOf := map[uint16]uint32{}
	seqsByTS := map[uint32][]uint16{}
	var repairTS []uint32
	for _, p := range pkts {
		if p.PayloadType == 96 {
			base := p.Timestamp - uint32(p.SequenceNumber%groupSize)*3000
			groupOf[p.SequenceNumber] = base
			seqsByTS[base] = append(seqsByTS[base], p.SequenceNumber)
			rec.ObserveMedia(base, p.SequenceNumber, p.Payload)
		} else {
			repairTS = append(repairTS, p.Timestamp)
		}
	}
	for _, ts := range repairTS {
		rec.AddRepair(ts, seqsByTS[ts], findParity(pkts, ts))
	}
	return func(seq uint16) (uint32, bool) {
		ts, ok := groupOf[seq]
		return ts, ok
	}
}

func findParity(pkts []*rtp.Packet, ts uint32) []byte {
	for _, p := range pkts {
		if p.PayloadType == 97 && p.Timestamp == ts {
			return p.Payload
		}
	}
	return nil
}
