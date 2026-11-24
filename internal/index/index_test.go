package index_test

import (
	"bytes"
	"testing"

	"mediaroute/internal/fixture"
	"mediaroute/internal/index"
	"mediaroute/internal/ingest"
	"mediaroute/internal/model"
	"mediaroute/internal/timeline"
)

func repaired(t *testing.T, d fixture.Damage) *model.SourceSet {
	t.Helper()
	g, err := fixture.Generate(fixture.DefaultParams(17), d)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	set, err := ingest.Read(g.Bytes, ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := timeline.Repair(set); err != nil {
		t.Fatalf("repair: %v", err)
	}
	return set
}

func TestBuildIndexFromCleanStream(t *testing.T) {
	set := repaired(t, fixture.DamageNone)
	var buf bytes.Buffer
	d, err := index.WriteRepaired(&buf, set.Init.Bytes, set.Fragments, index.WriteOptions{})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if d.Index.Len() != len(set.Fragments)*2 {
		t.Fatalf("want one entry per fragment per track (%d), got %d",
			len(set.Fragments)*2, d.Index.Len())
	}
	if d.Sha256 == "" {
		t.Fatal("delivery digest is empty")
	}
	// Every entry must be a random access point (video first sample, audio all).
	for _, e := range d.Index.Entries {
		if !e.RandomAccess {
			t.Fatalf("entry for track %d sequence %d is not random access", e.TrackID, e.Sequence)
		}
	}
	// Offsets must land on the recorded boundaries.
	bs := index.NewBoundarySet(d.Index.Boundaries)
	for _, e := range d.Index.Entries {
		if !bs.Contains(e.MoofOffset) || !bs.Contains(e.MdatOffset) {
			t.Fatalf("entry offset not on a box boundary: %+v", e)
		}
	}
}

func TestIndexValidationAcceptsRebuiltIndex(t *testing.T) {
	set := repaired(t, fixture.DamageNone)
	var buf bytes.Buffer
	d, err := index.WriteRepaired(&buf, set.Init.Bytes, set.Fragments, index.WriteOptions{})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	v, err := index.Validate(d.Index, nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if v.ValidEntries != d.Index.Len() || len(v.BadOffsets) != 0 {
		t.Fatalf("want all %d entries valid, got %d (bad %v)", d.Index.Len(), v.ValidEntries, v.BadOffsets)
	}
}

func TestIndexValidationRejectsOffBoundaryOffset(t *testing.T) {
	set := repaired(t, fixture.DamageNone)
	var buf bytes.Buffer
	d, err := index.WriteRepaired(&buf, set.Init.Bytes, set.Fragments, index.WriteOptions{})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	// Corrupt one entry's moof offset so it lands inside a box.
	bad := *d.Index
	bad.Entries = append([]model.IndexEntry(nil), d.Index.Entries...)
	bad.Entries[0].MoofOffset += 4
	v, err := index.Validate(&bad, nil)
	if err != nil {
		t.Fatalf("validate returned fatal error: %v", err)
	}
	if len(v.BadOffsets) != 1 {
		t.Fatalf("want 1 bad offset, got %d", len(v.BadOffsets))
	}
	if v.ValidEntries != d.Index.Len()-1 {
		t.Fatalf("want %d valid entries, got %d", d.Index.Len()-1, v.ValidEntries)
	}
}

func TestIndexValidationRejectsOverlap(t *testing.T) {
	ix := &model.Index{
		Boundaries: []model.ByteRange{{Start: 0, End: 100}, {Start: 100, End: 200}},
		Entries: []model.IndexEntry{
			{TrackID: 1, MoofOffset: 0, MdatOffset: 100, StartMicros: 0, EndMicros: 1000},
			{TrackID: 1, MoofOffset: 100, MdatOffset: 200, StartMicros: 500, EndMicros: 1500},
		},
	}
	if _, err := index.Validate(ix, nil); err == nil {
		t.Fatal("expected overlap to be rejected")
	}
}

func TestIndexValidationRejectsDuplicateEntry(t *testing.T) {
	ix := &model.Index{
		Boundaries: []model.ByteRange{{Start: 0, End: 100}},
		Entries: []model.IndexEntry{
			{TrackID: 1, MoofOffset: 0, MdatOffset: 50, StartMicros: 0, EndMicros: 1000},
			{TrackID: 1, MoofOffset: 0, MdatOffset: 50, StartMicros: 1000, EndMicros: 2000},
		},
	}
	if _, err := index.Validate(ix, nil); err == nil {
		t.Fatal("expected duplicate entry to be rejected")
	}
}

func TestIndexDropsDiscardedFragments(t *testing.T) {
	set := repaired(t, fixture.DamageTruncatedBox)
	var buf bytes.Buffer
	d, err := index.WriteRepaired(&buf, set.Init.Bytes, set.Fragments, index.WriteOptions{})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if d.Index.Len() != (len(set.Fragments)-1)*2 {
		t.Fatalf("want %d entries (dropping one fragment), got %d",
			(len(set.Fragments)-1)*2, d.Index.Len())
	}
	// No entry may reference the dropped fragment's time span.
	for _, e := range d.Index.Entries {
		if e.Sequence == 4 && e.TrackID == 1 {
			t.Fatalf("dropped fragment leaked into the index: %+v", e)
		}
	}
}

func TestWriteRepairedRewritesTfdt(t *testing.T) {
	set := repaired(t, fixture.DamageTfdtRegression)
	var buf bytes.Buffer
	if _, err := index.WriteRepaired(&buf, set.Init.Bytes, set.Fragments, index.WriteOptions{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Re-read the repaired output: tfdt regressions must be gone.
	again, err := ingest.Read(buf.Bytes(), ingest.DefaultConfig())
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	res, err := timeline.Repair(again)
	if err != nil {
		t.Fatalf("re-repair: %v", err)
	}
	if res.RepairedTfdt != 0 {
		t.Fatalf("repaired output still needs %d tfdt repairs", res.RepairedTfdt)
	}
}

func TestWriteRepairedIsByteStable(t *testing.T) {
	set := repaired(t, fixture.DamageNone)
	var a, b bytes.Buffer
	da, err := index.WriteRepaired(&a, set.Init.Bytes, set.Fragments, index.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := index.WriteRepaired(&b, set.Init.Bytes, set.Fragments, index.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("repeated writes produced different bytes")
	}
	if da.Sha256 != db.Sha256 {
		t.Fatal("repeated writes produced different digests")
	}
}

func TestWriteRepairedSetPreservesUnknownBoxes(t *testing.T) {
	set := repaired(t, fixture.DamageNone)
	// Inject an unknown box between init and the first fragment.
	unknown := make([]byte, 16)
	unknown[0], unknown[1], unknown[2], unknown[3] = 0, 0, 0, 16
	copy(unknown[4:], "zzzz")
	set.Preserved = []model.PreservedBox{{Type: "zzzz", Bytes: unknown}}
	var buf bytes.Buffer
	d, err := index.WriteRepairedSet(&buf, set, index.WriteOptions{})
	if err != nil {
		t.Fatalf("write set: %v", err)
	}
	out := buf.Bytes()
	if !bytes.Contains(out, []byte("zzzz")) {
		t.Fatal("unknown box was not preserved in the delivered output")
	}
	// The preserved box must be registered as a boundary so later offsets
	// remain valid.
	bs := index.NewBoundarySet(d.Index.Boundaries)
	for _, e := range d.Index.Entries {
		if !bs.Contains(e.MoofOffset) {
			t.Fatalf("index offset %d not on a boundary after preserving a box", e.MoofOffset)
		}
	}
}
