package pipeline

import (
	"testing"

	"mediaqc/internal/clock"
	"mediaqc/internal/model"
	"mediaqc/internal/recovery"
)

func mkEvent(seq uint64, payload []byte) model.Event {
	tb, _ := clock.New(90000)
	return model.Event{
		MaterialID: "m", TrackID: "t", Sequence: seq, Payload: payload,
		Timebase: tb, Start: tb.At(int64(seq) * 3000), Duration: tb.At(3000),
		PTS: tb.At(int64(seq) * 3000),
	}
}

func TestDeterministicRerunIdentical(t *testing.T) {
	mats := []Material{{
		ID: "mat-det",
		Events: []model.Event{
			mkEvent(0, []byte{1}), mkEvent(2, []byte{3}), // seq 1 gap
		},
		Gaps:      []model.Gap{{FirstSeq: 1, LastSeq: 1, TrackID: "t", DeclaredBy: model.StageParse}},
		FECGroups: []recovery.Group{{GroupID: 1, Members: []uint64{0, 1, 2},
			Parity: recovery.Parity([][]byte{{1}, {2}, {3}})}},
	}}
	run := func() ([]MaterialResult, string) {
		b := NewBatch(Config{WindowSize: 8, ToleranceMs: 20})
		res := b.Run(mats)
		return res, b.CheckpointDigest()
	}
	res1, hash1 := run()
	res2, hash2 := run()
	if hash1 != hash2 {
		t.Fatalf("checkpoint digest differs across reruns: %s vs %s", hash1, hash2)
	}
	if res1[0].Status != res2[0].Status || len(res1[0].OutputSequences) != len(res2[0].OutputSequences) {
		t.Fatal("rerun summary must be identical")
	}
}

func TestSingleLossRecoveredThroughPipeline(t *testing.T) {
	mats := []Material{{
		ID:     "mat-fec-ok",
		Events: []model.Event{mkEvent(0, []byte{10}), mkEvent(2, []byte{30})},
		Gaps:   []model.Gap{{FirstSeq: 1, LastSeq: 1, TrackID: "t", DeclaredBy: model.StageBuffer}},
		FECGroups: []recovery.Group{{GroupID: 1, Members: []uint64{0, 1, 2},
			Parity: recovery.Parity([][]byte{{10}, {20}, {30}})}},
	}}
	b := NewBatch(Config{WindowSize: 8, ToleranceMs: 20})
	res := b.Run(mats)
	r := res[0]
	if r.Status == "failed" {
		t.Fatalf("single loss should recover, got failures %+v", r.InvariantFailures)
	}
	found := false
	for _, seq := range r.Recovered {
		if seq == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("seq 1 must be recovered, got %+v", r.Recovered)
	}
	if len(r.InvariantFailures) != 0 {
		t.Fatalf("invariants must hold: %+v", r.InvariantFailures)
	}
}

func TestDoubleLossPropagatesUnrecoverable(t *testing.T) {
	mats := []Material{{
		ID:     "mat-fec-bad",
		Events: []model.Event{mkEvent(2, []byte{3})}, // seq 0,1 lost
		Gaps:   []model.Gap{{FirstSeq: 0, LastSeq: 1, TrackID: "t", DeclaredBy: model.StageBuffer}},
		FECGroups: []recovery.Group{{GroupID: 1, Members: []uint64{0, 1, 2},
			Parity: recovery.Parity([][]byte{{1}, {2}, {3}})}},
	}}
	b := NewBatch(Config{WindowSize: 16, ToleranceMs: 20})
	res := b.Run(mats)
	r := res[0]
	if r.Status != "degraded" && r.Status != "failed" {
		t.Fatalf("double loss must mark material degraded/failed, got %s", r.Status)
	}
	if len(r.Recovered) != 0 {
		t.Fatal("double loss must not recover any packet")
	}
	if len(r.UnrecoveredGaps) == 0 {
		t.Fatal("unrecovered gap must propagate to result")
	}
}

func TestErrorIsolationBetweenMaterials(t *testing.T) {
	good := Material{ID: "good", Events: []model.Event{mkEvent(0, []byte{1}), mkEvent(1, []byte{2})}}
	bad := Material{ID: "bad", Events: []model.Event{mkEvent(0, []byte{1})},
		Gaps: []model.Gap{{FirstSeq: 5, LastSeq: 6, TrackID: "t", DeclaredBy: model.StageBuffer}}}
	b := NewBatch(Config{WindowSize: 4, ToleranceMs: 20})
	res := b.Run([]Material{good, bad})
	if len(res) != 2 {
		t.Fatal("both materials must produce a result")
	}
	if res[0].Status != "ok" {
		t.Fatalf("good material must stay clean, got %s (%+v)", res[0].Status, res[0].Errors)
	}
	if res[1].Status == "ok" {
		t.Fatal("bad material must carry its own failure")
	}
}

func TestInvariantRecoveredWithoutGapFails(t *testing.T) {
	iv := NewInvariants()
	source := map[uint64]bool{0: true}
	recovered := mkEvent(1, nil)
	recovered.Recovered = true
	recovered.HasGapRef = false
	output := []model.Event{mkEvent(0, []byte{1}), recovered}
	iv.Check("mat-iv", source, output, nil, false, true)
	if !iv.Failed() {
		t.Fatal("recovered event without gap reference must violate an invariant")
	}
}

func TestInvariantProvenanceAndMonotonic(t *testing.T) {
	iv := NewInvariants()
	// output seq 9 has no source event and is not recovered.
	output := []model.Event{mkEvent(0, nil), mkEvent(9, nil)}
	iv.Check("mat-prov", map[uint64]bool{0: true}, output, nil, false, true)
	if len(iv.Failures) != 1 {
		t.Fatalf("want exactly one provenance failure, got %+v", iv.Failures)
	}
}

func TestCheckpointRejectsInconsistentOverwrite(t *testing.T) {
	cp := NewCheckpointer()
	a := Checkpoint{MaterialID: "m", Stage: model.StageBuffer, EventCount: 1, Sequences: []uint64{0}}
	if err := cp.Save(a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.EventCount = 2
	if err := cp.Save(b); err == nil {
		t.Fatal("non-deterministic overwrite must be rejected")
	}
}
