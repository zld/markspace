package timeline

import (
	"testing"

	"mediarc/internal/model"
)

func msEvent(id string, ms int64) Event {
	return Event{ID: id, TrackID: 1, PTS: model.Milliseconds(ms), Payload: []byte(id)}
}

func TestWatermarkCommitsAndDeterministicReplay(t *testing.T) {
	run := func() Result {
		e := NewEngine(40, 120)
		input := []Event{msEvent("a", 0), msEvent("c", 80), msEvent("b", 40), msEvent("d", 120)}
		var res *Result
		var err error
		for _, ev := range input {
			res, err = e.Add(ev)
			if err != nil {
				t.Fatal(err)
			}
		}
		return *res
	}
	r1, r2 := run(), run()
	if len(r1.OrderedIDs) != 4 || r1.OrderedIDs[0] != "a" || r1.OrderedIDs[1] != "b" {
		t.Fatalf("bad order: %v", r1.OrderedIDs)
	}
	if r1.RewindCount == 0 {
		t.Fatal("out-of-order event did not trigger rewind")
	}
	if r1.OrderHash != r2.OrderHash {
		t.Fatalf("nondeterministic hash: %s vs %s", r1.OrderHash, r2.OrderHash)
	}
	if len(r1.Revisions) != len(r2.Revisions) {
		t.Fatal("revision counts differ")
	}
}

func TestLateEventBeyondBoundary(t *testing.T) {
	e := NewEngine(30, 100)
	if _, err := e.Add(msEvent("a", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Add(msEvent("mid", 160)); err != nil {
		t.Fatal(err)
	}
	before := e.Result().RewindCount
	_, err := e.Add(msEvent("late", 10)) // gap 150 > 100
	if !model.HasCode(err, model.CodeLateBoundary) {
		t.Fatalf("want late boundary, got %v", err)
	}
	me, _ := model.AsMediaError(err)
	if me.SourceOffset == nil {
		t.Fatal("error lacks offset")
	}
	if e.Result().RewindCount != before {
		t.Fatal("rejected event must not mutate rewind count")
	}
}

func TestDuplicateEventIdempotent(t *testing.T) {
	e := NewEngine(10, 20)
	_, _ = e.Add(msEvent("a", 0))
	r, err := e.Add(msEvent("a", 0))
	if err != nil || len(r.OrderedIDs) != 1 {
		t.Fatalf("duplicate changed result: %+v err=%v", r, err)
	}
}
