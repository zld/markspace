package recovery

import (
	"bytes"
	"testing"

	"mediaqc/internal/model"
)

func makeEvent(seq uint64, payload []byte) model.Event {
	return model.Event{MaterialID: "m", TrackID: "t", Sequence: seq, Payload: payload, Size: len(payload)}
}

func TestParityRoundTrip(t *testing.T) {
	payloads := [][]byte{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}
	parity := Parity(payloads)
	// Recover first member: parity XOR the other two.
	got := xorInto(append([]byte(nil), parity...), payloads[1])
	got = xorInto(got, payloads[2])
	if !bytes.Equal(got, payloads[0]) {
		t.Fatalf("XOR recovery mismatch: %v", got)
	}
}

func TestSingleLossRecovered(t *testing.T) {
	d := NewDecoder("mat-fec", "t")
	p0, p1, p2 := []byte{10, 20}, []byte{30, 40}, []byte{50, 60}
	group := Group{GroupID: 1, Members: []uint64{0, 1, 2}, Parity: Parity([][]byte{p0, p1, p2})}
	if err := d.AddGroup(group); err != nil {
		t.Fatal(err)
	}
	d.Observe(makeEvent(0, p0))
	// seq 1 lost
	d.Observe(makeEvent(2, p2))

	gaps := []model.Gap{{TrackID: "t", FirstSeq: 1, LastSeq: 1, DeclaredBy: model.StageParse}}
	recovered, outGaps, errs := d.Recover(gaps)
	if len(errs) != 0 || len(recovered) != 1 {
		t.Fatalf("single loss must recover, errs=%+v recovered=%d", errs, len(recovered))
	}
	ev := recovered[0]
	if ev.Sequence != 1 || !ev.Recovered || !ev.HasGapRef || ev.FillsGapSeq != 1 {
		t.Fatalf("recovered event provenance wrong: %+v", ev)
	}
	if !bytes.Equal(ev.Payload, p1) {
		t.Fatalf("payload = %v, want %v", ev.Payload, p1)
	}
	if !outGaps[0].Recovered {
		t.Fatal("declared gap must be marked recovered")
	}
	st := d.Stats()
	if st.RecoveryRate != 1.0 || st.RecoveredPackets != 1 {
		t.Fatalf("recovery rate = %f, want 1.0", st.RecoveryRate)
	}
}

func TestDoubleLossUnrecoverable(t *testing.T) {
	d := NewDecoder("mat-fec2", "t")
	p0, p1, p2 := []byte{1}, []byte{2}, []byte{3}
	_ = d.AddGroup(Group{GroupID: 7, Members: []uint64{0, 1, 2}, Parity: Parity([][]byte{p0, p1, p2})})
	d.Observe(makeEvent(2, p2)) // 0 and 1 lost
	gaps := []model.Gap{{FirstSeq: 0, LastSeq: 1, TrackID: "t", DeclaredBy: model.StageBuffer}}

	recovered, _, errs := d.Recover(gaps)
	if len(recovered) != 0 {
		t.Fatal("double loss must not produce recovered events")
	}
	if len(errs) != 1 || !model.HasCode(errs[0], model.CodeUnrecoverableLoss) {
		t.Fatalf("want one unrecoverable error, got %+v", errs)
	}
	if model.IsRecoverable(errs[0]) {
		t.Fatal("double loss must be unrecoverable")
	}
	if d.Stats().RecoveryRate != 0.0 || d.Stats().UnrecoveredPackets != 2 {
		t.Fatalf("stats wrong: %+v", d.Stats())
	}
}

func TestRecoveryOnlyForDeclaredGap(t *testing.T) {
	d := NewDecoder("mat-fec3", "t")
	p0, p1 := []byte{9, 9}, []byte{1, 1}
	_ = d.AddGroup(Group{GroupID: 3, Members: []uint64{0, 1}, Parity: Parity([][]byte{p0, p1})})
	d.Observe(makeEvent(0, p0))
	// seq 1 missing but NOT declared as a gap
	_, _, errs := d.Recover(nil)
	if len(errs) != 1 || !model.HasCode(errs[0], model.CodeInvariant) {
		t.Fatalf("undeclared recovery must violate invariant, got %+v", errs)
	}
}

func TestGroupValidation(t *testing.T) {
	d := NewDecoder("m", "t")
	if err := d.AddGroup(Group{GroupID: 1, Members: []uint64{0}}); err == nil {
		t.Fatal("single-member group must be rejected")
	}
}

func TestNoLossMeansRateZero(t *testing.T) {
	d := NewDecoder("m", "t")
	_ = d.AddGroup(Group{GroupID: 1, Members: []uint64{0, 1}, Parity: Parity([][]byte{{1}, {2}})})
	d.Observe(makeEvent(0, []byte{1}))
	d.Observe(makeEvent(1, []byte{2}))
	recovered, _, errs := d.Recover(nil)
	if len(recovered) != 0 || len(errs) != 0 || d.Stats().RecoveryRate != 0 {
		t.Fatalf("no-loss run must be clean: recovered=%d errs=%d stats=%+v",
			len(recovered), len(errs), d.Stats())
	}
}
