package mediacore

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"
)

func TestJournalCommitIdempotentAndStable(t *testing.T) {
	j := NewJournal()
	keys := []Key{{1, 0}, {1, 1}}
	j.Commit(keys)
	first := j.Committed()
	// A recovery run re-commits the same slots plus a duplicate request.
	j.Commit(keys)
	j.Commit([]Key{{1, 0}})
	second := j.Committed()
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("commit must be idempotent: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("committed ordering/content drifted: %v vs %v", first, second)
		}
	}
}

func TestPublishBatchRollsBackOnFailure(t *testing.T) {
	j := NewJournal()
	keys := []Key{{1, 0}, {1, 1}, {1, 2}}
	boom := WrapMsg(ErrConflict, "a", 1, StageMerge, 512, "simulated half-finished batch")
	recs, err := j.PublishBatch(keys, func() error { return boom })
	if err == nil {
		t.Fatal("expected batch failure")
	}
	if len(j.Committed()) != 0 {
		t.Fatalf("no half-finished batch may remain committed: %v", j.Committed())
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 rollback records, got %d", len(recs))
	}
	for _, rec := range recs {
		if rec.Stage != StageMerge || rec.Offset != 512 {
			t.Fatalf("rollback record must carry stage/offset: %+v", rec)
		}
	}
	// A successful retry commits cleanly and leaves no residue of the failure.
	recs2, err2 := j.PublishBatch(keys, func() error { return nil })
	if err2 != nil || len(recs2) != 0 || len(j.Committed()) != 3 {
		t.Fatalf("retry should fully commit: err=%v recs=%v committed=%v", err2, recs2, j.Committed())
	}
}

func TestJournalSaveLoadRecoverySummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	j := NewJournal()
	j.Commit([]Key{{2, 4}, {2, 5}})
	j.Rollback([]Key{{2, 4}}, StageGraph, 99, "cycle", 1000)
	if err := j.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Committed()) != 1 || loaded.Committed()[0] != (Key{2, 5}) {
		t.Fatalf("recovered committed set wrong: %v", loaded.Committed())
	}
	rb := loaded.Rollbacks()
	if len(rb) != 1 || rb[0].Stage != StageGraph || rb[0].Offset != 99 {
		t.Fatalf("rollback history not recovered: %+v", rb)
	}
	// Missing journal file is an empty ledger, not a failure.
	if empty, err := LoadJournal(filepath.Join(t.TempDir(), "absent.json")); err != nil || len(empty.Committed()) != 0 {
		t.Fatalf("absent journal must load empty: err=%v", err)
	}
}

func TestGraphCycleAndTopoOrder(t *testing.T) {
	// 1 -> 2 -> 3 -> 1 forms a cycle; 4 depends on 1.
	decls := []*TrackDecl{
		{ID: 1, DependsOn: []uint32{3}},
		{ID: 2, DependsOn: []uint32{1}},
		{ID: 3, DependsOn: []uint32{2}},
		{ID: 4, DependsOn: []uint32{1}},
	}
	_, err := ValidateGraph("a", decls)
	if err == nil || !errors.Is(err, ErrCycle) {
		t.Fatalf("cycle must be reported: %v", err)
	}
	var me *Error
	if !errors.As(err, &me) || me.Stage != StageGraph {
		t.Fatalf("cycle error must be graph-staged: %v", err)
	}
	// Acyclic version: dependencies always precede dependents.
	ok := []*TrackDecl{
		{ID: 1}, {ID: 2, DependsOn: []uint32{1}}, {ID: 3, DependsOn: []uint32{2}},
	}
	order, err := ValidateGraph("a", ok)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[uint32]int{}
	for i, id := range order {
		pos[id] = i
	}
	if !(pos[1] < pos[2] && pos[2] < pos[3]) {
		t.Fatalf("topological order violated: %v", order)
	}
}

func TestRTPSequenceAnomalies(t *testing.T) {
	ssrc := uint32(4242)
	stream := []*rtp.Packet{
		NewRTPPacket(ssrc, 10, 1000, 1),
		NewRTPPacket(ssrc, 11, 2000, 2),
		NewRTPPacket(ssrc, 14, 3000, 3), // gap: expected 12
		NewRTPPacket(ssrc, 14, 4000, 4), // duplicate
	}
	raw, err := BuildRTPCapture(stream)
	if err != nil {
		t.Fatal(err)
	}
	_, anomalies, err := InspectRTPStream("a", bytes.NewReader(raw), NanosPerSecond/50)
	if err != nil {
		t.Fatal(err)
	}
	if len(anomalies) != 2 {
		t.Fatalf("want gap+duplicate, got %+v", anomalies)
	}
	kinds := map[string]bool{}
	for _, a := range anomalies {
		kinds[a.Kind] = true
		if a.EndNS <= a.StartNS {
			t.Fatalf("anomaly interval inverted: %+v", a)
		}
	}
	if !kinds["gap"] || !kinds["duplicate"] {
		t.Fatalf("anomaly kinds wrong: %v", kinds)
	}
}

func TestSegmentRejectsMissingTimescale(t *testing.T) {
	raw, err := BuildFragment(FragmentSpec{TrackID: 1, SampleDeltas: []uint32{100},
		Payload: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSegment("a", 1, 0, bytes.NewReader(raw)); err == nil ||
		!errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("zero-timescale fragment must be rejected: %v", err)
	}
}
