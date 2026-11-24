package index

import (
	"errors"
	"testing"

	"mediarun/internal/parse"
)

func src(path string, base uint64, syncEvery, n int, dur uint32) Source {
	f := &parse.Fragment{Path: path, MoofOffset: 24}
	rep := make([]uint64, n)
	for i := 0; i < n; i++ {
		f.Samples = append(f.Samples, parse.Sample{
			Index:      i,
			DecodeTime: base + uint64(i)*uint64(dur),
			Duration:   dur,
			Size:       10,
			Sync:       syncEvery > 0 && i%syncEvery == 0,
			DataOffset: int64(1000 + i*10),
		})
		rep[i] = base + uint64(i)*uint64(dur)
	}
	return Source{Fragment: f, Repaired: rep, Timescale: 90000, Digest: "deadbeef"}
}

func TestBuildAndQuery(t *testing.T) {
	// Two fragments, 10 samples of 3000 units (1/30 s) each, sync every 5.
	ix, err := Build([]Source{
		src("b.m4s", 30000, 5, 10, 3000),
		src("a.m4s", 0, 5, 10, 3000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Entries) != 4 {
		t.Fatalf("entries = %d", len(ix.Entries))
	}
	// Entries sorted by tick regardless of source order.
	if ix.Entries[0].Fragment != "a.m4s" || ix.Entries[0].Tick != 0 {
		t.Fatalf("first entry: %+v", ix.Entries[0])
	}
	// Query at 1.5 * 3000 ticks lands on sample 0 (tick 0) of a.m4s? No:
	// sync points are at ticks 0, 15000, 30000, 45000.
	e, err := ix.Query(14999)
	if err != nil {
		t.Fatal(err)
	}
	if e.Tick != 0 || e.SampleIndex != 0 {
		t.Fatalf("query 14999 -> %+v", e)
	}
	e, err = ix.Query(15000)
	if err != nil {
		t.Fatal(err)
	}
	if e.Tick != 15000 || e.SampleIndex != 5 || e.Fragment != "a.m4s" {
		t.Fatalf("query 15000 -> %+v", e)
	}
	if e.DataOffset != 1050 || e.MoofOffset != 24 || e.SourceDigest != "deadbeef" {
		t.Fatalf("entry missing source info: %+v", e)
	}
	// End = last sample end = 60000 ticks.
	if ix.End != 60000 {
		t.Fatalf("end = %d", ix.End)
	}
	e, err = ix.Query(59999)
	if err != nil {
		t.Fatal(err)
	}
	if e.Tick != 45000 {
		t.Fatalf("query 59999 -> %+v", e)
	}
}

func TestQueryBoundaries(t *testing.T) {
	ix, err := Build([]Source{src("a.m4s", 0, 5, 10, 3000)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Query(-1); !errors.Is(err, ErrBeforeStart) {
		t.Fatalf("before start: %v", err)
	}
	if _, err := ix.Query(60000); !errors.Is(err, ErrAfterEnd) {
		t.Fatalf("at end: %v", err)
	}
	if _, err := ix.Query(1 << 40); !errors.Is(err, ErrAfterEnd) {
		t.Fatalf("past end: %v", err)
	}
}

func TestBuildMismatch(t *testing.T) {
	s := src("a.m4s", 0, 1, 3, 3000)
	s.Repaired = s.Repaired[:2]
	if _, err := Build([]Source{s}); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestBuildNoSync(t *testing.T) {
	s := src("a.m4s", 0, 0, 3, 3000) // no sync samples
	if _, err := Build([]Source{s}); err == nil {
		t.Fatal("expected no-RAP error")
	}
}
