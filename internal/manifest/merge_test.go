package manifest

import (
	"bytes"
	"testing"
)

func TestMergeFirstRunThenRerun(t *testing.T) {
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	fresh, err := adapter.Convert(old, resolvedFor(old))
	if err != nil {
		t.Fatal(err)
	}
	first, counts, err := Merge(nil, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Added != 2 || counts.Reused != 0 {
		t.Fatalf("first run counts %+v", counts)
	}
	// A second, independently built but identical release must reuse both.
	fresh2, err := adapter.Convert(old, resolvedFor(old))
	if err != nil {
		t.Fatal(err)
	}
	again, counts2, err := Merge(first, fresh2)
	if err != nil {
		t.Fatal(err)
	}
	if counts2.Added != 0 || counts2.Reused != 2 || counts2.Applied() {
		t.Fatalf("rerun counts %+v", counts2)
	}
	if again != first {
		t.Fatal("no-op merge should return the committed manifest unchanged")
	}
	if again.Digest != first.Digest || again.Sequence != 1 {
		t.Fatal("no-op merge perturbed the manifest")
	}
}

func TestMergeNeverDuplicates(t *testing.T) {
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	fresh, _ := adapter.Convert(old, resolvedFor(old))
	base, _, _ := Merge(nil, fresh)
	// Three consecutive merges of the same fresh manifest.
	cur := base
	for i := 0; i < 3; i++ {
		next, counts, err := Merge(cur, fresh)
		if err != nil {
			t.Fatal(err)
		}
		if counts.Added != 0 || counts.Reused != 2 {
			t.Fatalf("iteration %d duplicated resources: %+v", i, counts)
		}
		cur = next
	}
	if len(cur.Resources) != 2 {
		t.Fatalf("resource count grew to %d", len(cur.Resources))
	}
}

func TestMergeReplacesChangedResource(t *testing.T) {
	adapter := &Adapter{Release: "spring", Target: TargetSpec{Rate: 48000, Layout: "stereo", LUFS: -16}}
	old, _ := ReadV1(bytes.NewReader(legacyJSON()))
	fresh, _ := adapter.Convert(old, resolvedFor(old))
	base, _, _ := Merge(nil, fresh)
	changed := fresh.Clone()
	changed.Resources[0].SHA256 = digest('9')
	changed.Resources[0].Bytes++
	next, counts, err := Merge(base, changed)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Replaced != 1 || counts.Reused != 1 || counts.Added != 0 {
		t.Fatalf("replace counts %+v", counts)
	}
	if len(next.Resources) != 2 || next.Sequence != base.Sequence+1 {
		t.Fatalf("replace shape wrong: seq=%d n=%d", next.Sequence, len(next.Resources))
	}
}
