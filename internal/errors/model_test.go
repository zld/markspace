package mpierr

import (
	"errors"
	"testing"
)

func TestCategoryStableNames(t *testing.T) {
	cases := map[Category]string{
		CatGap: "gap", CatCorrupt: "corrupt", CatTruncated: "truncated",
		CatLate: "late", CatDuplicate: "duplicate", CatSync: "sync",
		CatDAG: "dag", CatLimit: "limit", CatCheckpoint: "checkpoint",
		CatUnknown: "unknown",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("category %d = %q, want %q", c, got, want)
		}
	}
}

func TestMediaErrorFields(t *testing.T) {
	base := errors.New("crc mismatch")
	e := New(CatCorrupt, 42, "recover", "block check failed").
		AtOffset(137).Wrap(base)
	if e.FirstSeq != 42 || e.ByteOffset != 137 || e.Node != "recover" || e.Category != CatCorrupt {
		t.Fatalf("unexpected error fields: %+v", e)
	}
	if !errors.Is(e, base) {
		t.Fatal("errors.Is must reach wrapped cause")
	}
	if got := e.Error(); got == "" {
		t.Fatal("empty error string")
	}
}

func TestDefaultOffsetUnknown(t *testing.T) {
	e := New(CatGap, 7, "", "missing packet")
	if e.ByteOffset != -1 {
		t.Fatalf("default offset = %d, want -1", e.ByteOffset)
	}
	e2 := e.WithNode("jitter")
	if e2.Node != "jitter" || e2 != e {
		t.Fatal("WithNode must mutate and return same error")
	}
}
