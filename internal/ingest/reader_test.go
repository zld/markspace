package ingest_test

import (
	"testing"

	"mediaroute/internal/fixture"
	"mediaroute/internal/ingest"
	"mediaroute/internal/model"
)

func generate(t *testing.T, d fixture.Damage) *fixture.Generated {
	t.Helper()
	g, err := fixture.Generate(fixture.DefaultParams(17), d)
	if err != nil {
		t.Fatalf("generate %s: %v", d, err)
	}
	return g
}

func TestReadCleanStream(t *testing.T) {
	g := generate(t, fixture.DamageNone)
	set, err := ingest.Read(g.Bytes, ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := set.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(set.Tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(set.Tracks))
	}
	if len(set.Fragments) != g.Params.Fragments {
		t.Fatalf("want %d fragments, got %d", g.Params.Fragments, len(set.Fragments))
	}
	for _, f := range set.Fragments {
		if f.Truncated || f.Discarded {
			t.Fatalf("clean fragment %d marked damaged", f.Index)
		}
		if len(f.Tracks) != 2 {
			t.Fatalf("fragment %d has %d tracks", f.Index, len(f.Tracks))
		}
		for _, ft := range f.Tracks {
			if len(ft.Samples) != g.Params.SamplesPerFrag {
				t.Fatalf("fragment %d track %d has %d samples", f.Index, ft.TrackID, len(ft.Samples))
			}
			if ft.Samples[0].IsSync != true {
				t.Fatalf("first sample of track %d should be a sync sample", ft.TrackID)
			}
			if ft.TrackID == 1 {
				// Video: only the leading sample of each fragment is a random
				// access point.
				for i := 1; i < len(ft.Samples); i++ {
					if ft.Samples[i].IsSync {
						t.Fatalf("video sample %d should not be a sync sample", i)
					}
				}
			}
		}
	}
}

func TestReadTruncatedIsolatesAndRecovers(t *testing.T) {
	g := generate(t, fixture.DamageTruncatedBox)
	set, err := ingest.Read(g.Bytes, ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var truncated, recovered int
	for _, f := range set.Fragments {
		if f.Truncated {
			truncated++
			if !f.Discarded {
				t.Fatalf("truncated fragment %d must be discarded", f.Index)
			}
		}
		if f.Recovered {
			recovered++
		}
	}
	if truncated != 1 {
		t.Fatalf("want 1 truncated fragment, got %d", truncated)
	}
	if recovered < 1 {
		t.Fatalf("want at least one recovered fragment, got %d", recovered)
	}
	// Later fragments must still parse with full sample data.
	full := 0
	for _, f := range set.Fragments {
		if !f.Discarded && !f.Truncated {
			full++
		}
	}
	if full != g.Params.Fragments-1 {
		t.Fatalf("want %d intact fragments, got %d", g.Params.Fragments-1, full)
	}
}

func TestReadReportsTruncationIssue(t *testing.T) {
	g := generate(t, fixture.DamageTruncatedBox)
	set, _ := ingest.Read(g.Bytes, ingest.DefaultConfig())
	found := false
	for _, iss := range set.Issues {
		if iss.Category == model.CategoryTruncatedBox {
			found = true
			if iss.Available == 0 || iss.DeclaredSize == 0 {
				t.Fatalf("truncation issue lacks numbers: %+v", iss)
			}
		}
	}
	if !found {
		t.Fatal("no truncated_box issue recorded")
	}
}

func TestReadMissingInitFailsValidation(t *testing.T) {
	g := generate(t, fixture.DamageMissingInit)
	set, err := ingest.Read(g.Bytes, ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("read should succeed with damaged fragments: %v", err)
	}
	verr := set.Validate()
	te, ok := model.AsTaskError(verr)
	if !ok || te.Category != model.CategoryMissingInit {
		t.Fatalf("want missing_init, got %v", verr)
	}
}

func TestReadZeroTimescaleFailsValidation(t *testing.T) {
	g := generate(t, fixture.DamageZeroTimescale)
	set, err := ingest.Read(g.Bytes, ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	verr := set.Validate()
	te, ok := model.AsTaskError(verr)
	if !ok || te.Category != model.CategoryZeroTimescale {
		t.Fatalf("want zero_timescale, got %v", verr)
	}
}

func TestReadCorruptIndexIsHintOnly(t *testing.T) {
	g := generate(t, fixture.DamageCorruptIndex)
	set, err := ingest.Read(g.Bytes, ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !set.DamagedIndex {
		t.Fatal("corrupt sidx offsets should mark the index damaged")
	}
	if len(set.IndexIssues) == 0 {
		t.Fatal("corrupt index hints should produce issues")
	}
	// The rebuilt fragment set must be identical in shape to a clean read.
	if len(set.Fragments) != g.Params.Fragments {
		t.Fatalf("want %d fragments, got %d", g.Params.Fragments, len(set.Fragments))
	}
	for _, f := range set.Fragments {
		if f.Truncated || f.Discarded {
			t.Fatalf("corrupt index must not damage media fragments (fragment %d)", f.Index)
		}
	}
	for _, iss := range set.IndexIssues {
		if iss.Category != model.CategoryCorruptIndex {
			t.Fatalf("unexpected category %s", iss.Category)
		}
	}
}
