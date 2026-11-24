package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediarc/internal/model"
)

func acceptSeq(t *testing.T, r *Recoverer, seq uint16) {
	t.Helper()
	p := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 3000}, Payload: []byte{byte(seq)}}
	if err := r.Accept(p, int64(seq)*10, false); err != nil {
		t.Fatal(err)
	}
}

func TestReorderDuplicateAndWrap(t *testing.T) {
	r := NewRecoverer(Config{Window: 16})
	for _, seq := range []uint16{65534, 65535, 2, 0, 1} {
		acceptSeq(t, r, seq)
	}
	rep := r.Finalize()
	if rep.Duplicates != 0 {
		t.Fatalf("unexpected duplicates: %+v", rep)
	}
	if rep.Reordered < 2 {
		t.Fatalf("expected reorder counts, got %+v", rep)
	}
	if r.Expand(0) <= r.Expand(65535) {
		t.Fatalf("wrap not expanded: %d then %d", r.Expand(65535), r.Expand(0))
	}
	acceptSeq(t, r, 0)
	if r.Finalize().Duplicates != 1 {
		t.Fatal("duplicate was not counted")
	}
}

func TestRecoveryInsideWindow(t *testing.T) {
	r := NewRecoverer(Config{Window: 8})
	lost := model.RTPPacket{Sequence: 3, Timestamp: 9000, Payload: []byte("lost")}
	r.NoteInjectedLoss(lost)
	for _, seq := range []uint16{1, 2, 4, 5} {
		acceptSeq(t, r, seq)
	}
	if err := r.AcceptModel(lost, true); err != nil {
		t.Fatal(err)
	}
	rep := r.Finalize()
	if rep.Recovered != 1 || rep.Unrecoverable != 0 || rep.RecoveryRate != 1 {
		t.Fatalf("bad recovery report: %+v", rep)
	}
}

func TestBurstLossOverWindow(t *testing.T) {
	r := NewRecoverer(Config{Window: 4})
	lostSeqs := []uint16{2, 3, 4, 5}
	for _, seq := range lostSeqs {
		r.NoteInjectedLoss(model.RTPPacket{Sequence: seq, Timestamp: uint32(seq) * 100, SourceOffset: int64(100 + seq)})
	}
	acceptSeq(t, r, 1)
	for _, seq := range []uint16{6, 7, 8, 9} {
		acceptSeq(t, r, seq) // advance cutoff across sequences 2..5
	}
	rep := r.Finalize()
	if rep.Unrecoverable != 4 {
		t.Fatalf("want four unrecoverable, got %+v", rep)
	}
	err := r.UnrecoverableError(rep)
	if !model.HasCode(err, model.CodeLossOverWindow) {
		t.Fatalf("want structured loss error, got %v", err)
	}
	me, _ := model.AsMediaError(err)
	if me.Unrecoverable != 4 || me.SourceOffset == nil || *me.SourceOffset != 102 {
		t.Fatalf("error lacks count/deterministic first offset: %+v", me)
	}
}
