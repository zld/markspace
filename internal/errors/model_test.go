package errors_test

import (
	"testing"

	merr "mediapack/internal/errors"
)

func TestBookAssignsIDsDefaultsAndCounts(t *testing.T) {
	b := merr.NewBook()
	i1 := b.Add(merr.Issue{Category: merr.CatCCJump, Offset: 10})
	i2 := b.Add(merr.Issue{Category: merr.CatPCRWrap, Offset: 20, Recovery: merr.StateRecovered})
	if i1.ID == "" || i1.Severity == "" || i1.Recovery != merr.StateOpen {
		t.Fatalf("defaults not applied: %+v", i1)
	}
	if i2.Recovery != merr.StateRecovered {
		t.Fatal("explicit recovery state must be preserved")
	}
	if len(b.All()) != 2 {
		t.Fatal("want 2 issues")
	}
	c := b.Counts()
	if c[merr.CatCCJump] != 1 || c[merr.CatPCRWrap] != 1 {
		t.Fatalf("counts wrong: %+v", c)
	}
	if n := b.MarkCategory(merr.CatCCJump, merr.StateRecovered); n != 1 {
		t.Fatalf("marked=%d want 1", n)
	}
	if b.Of(merr.CatCCJump)[0].Recovery != merr.StateRecovered {
		t.Fatal("category mark not applied")
	}
	if n := b.Mark(merr.StateIsolated, i1.ID); n != 1 {
		t.Fatalf("id mark=%d", n)
	}
	if got := b.Of(merr.CatCCJump)[0].Recovery; got != merr.StateIsolated {
		t.Fatalf("id mark state=%s", got)
	}
}

func TestTypedError(t *testing.T) {
	e := merr.New(merr.CatPathRejected, 7, "bad %s", "x")
	if e.Category != merr.CatPathRejected || e.Offset != 7 {
		t.Fatalf("typed error fields wrong: %+v", e)
	}
	if e.Error() == "" {
		t.Fatal("Error() empty")
	}
}
