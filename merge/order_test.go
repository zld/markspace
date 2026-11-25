package merge

import (
	"encoding/json"
	"errors"
	"testing"

	"mediad/media"
	"mediad/pool"
)

func res(seq int, start, end int64, err error) pool.Result {
	if err != nil {
		return pool.Result{Sequence: seq, Err: err}
	}
	return pool.Result{Sequence: seq, Value: seq * 7}
}

func TestCollectSortsBySampleStart(t *testing.T) {
	m := NewMerger(map[int][2]int64{
		0: {200, 300}, 1: {0, 100}, 2: {100, 200},
	})
	// Ingest in scrambled completion order.
	m.Ingest(res(2, 100, 200, nil))
	m.Ingest(res(0, 200, 300, nil))
	m.Ingest(res(1, 0, 100, nil))

	o := m.Collect()
	if len(o.Items) != 3 || len(o.Failures) != 0 {
		t.Fatalf("merge partition wrong: %+v", o)
	}
	wantSeq := []int{1, 2, 0}
	for i, it := range o.Items {
		if it.Sequence != wantSeq[i] {
			t.Fatalf("position %d sequence = %d, want %d", i, it.Sequence, wantSeq[i])
		}
		if !it.OK || it.Value != it.Sequence*7 {
			t.Fatalf("item value not preserved: %+v", it)
		}
	}
}

func TestCollectTieBreaksOnSequence(t *testing.T) {
	m := NewMerger(map[int][2]int64{5: {0, 50}, 2: {0, 60}})
	m.Ingest(res(5, 0, 50, nil))
	m.Ingest(res(2, 0, 60, nil))
	o := m.Collect()
	if o.Items[0].Sequence != 2 || o.Items[1].Sequence != 5 {
		t.Fatalf("tie-break wrong: %d, %d", o.Items[0].Sequence, o.Items[1].Sequence)
	}
}

func TestPartialFailuresRetained(t *testing.T) {
	boom := errors.New("decode failed")
	m := NewMerger(map[int][2]int64{
		0: {0, 100}, 1: {100, 200}, 2: {200, 300},
	})
	m.Ingest(res(0, 0, 100, nil))
	m.Ingest(res(1, 100, 200, boom))
	m.Ingest(res(2, 200, 300, nil))

	o := m.Collect()
	if len(o.Items) != 2 {
		t.Fatalf("successful items = %d, want 2", len(o.Items))
	}
	if len(o.Failures) != 1 || o.Failures[0].Sequence != 1 || o.Failures[0].Failure != "decode failed" {
		t.Fatalf("failure record wrong: %+v", o.Failures)
	}
	if len(o.Faults) != 1 || o.Faults[0].Category != media.CatWorkerFailed {
		t.Fatalf("worker fault wrong: %+v", o.Faults)
	}
	if o.Faults[0].SampleStart != 100 || o.Faults[0].SampleEnd != 200 {
		t.Fatalf("fault must carry the failed segment's sample span")
	}
	// Successful work must still be fully ordered.
	if o.Items[0].Sequence != 0 || o.Items[1].Sequence != 2 {
		t.Fatalf("successful ordering wrong: %+v", o.Items)
	}
}

func TestCollectDeterministicAcrossRuns(t *testing.T) {
	build := func() *Order {
		m := NewMerger(map[int][2]int64{
			0: {300, 400}, 1: {0, 100}, 2: {100, 200}, 3: {200, 300},
		})
		m.Ingest(res(3, 200, 300, errors.New("x")))
		m.Ingest(res(1, 0, 100, nil))
		m.Ingest(res(0, 300, 400, nil))
		m.Ingest(res(2, 100, 200, errors.New("y")))
		return m.Collect()
	}
	a, _ := json.Marshal(build())
	b, _ := json.Marshal(build())
	if string(a) != string(b) {
		t.Fatalf("reruns differ:\n%s\n%s", a, b)
	}
	o := build()
	if len(o.Failures) != 2 || o.Failures[0].Sequence != 2 || o.Failures[1].Sequence != 3 {
		t.Fatalf("failures not sequence-ordered: %+v", o.Failures)
	}
}

func TestUnknownSequenceDoesNotDrop(t *testing.T) {
	m := NewMerger(map[int][2]int64{})
	m.Ingest(res(99, 0, 0, nil))
	o := m.Collect()
	if len(o.Items) != 1 || o.Items[0].Sequence != 99 {
		t.Fatalf("unknown-sequence result dropped: %+v", o.Items)
	}
}
