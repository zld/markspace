package source

import "testing"

func TestSamePathReplacementChangeSet(t *testing.T) {
	old := Fingerprint{Path: "/a", ContentSHA256: "c1", StructureSHA256: "s1", MetadataVersion: "m1"}
	newContent := old
	newContent.ContentSHA256 = "c2"
	change := Diff(old, newContent)
	if !change.SamePath || !change.ContentReplaced {
		t.Fatalf("expected same-path content replacement: %+v", change)
	}

	newStructure := old
	newStructure.StructureSHA256 = "s2"
	change = Diff(old, newStructure)
	if change.ContentReplaced || !change.StructureChanged {
		t.Fatalf("unexpected structure change: %+v", change)
	}

	newMeta := old
	newMeta.MetadataVersion = "m2"
	change = Diff(old, newMeta)
	if change.ContentReplaced || change.StructureChanged || !change.MetadataChanged {
		t.Fatalf("unexpected metadata-only change: %+v", change)
	}
}

func TestTrackerObservesPathReplacement(t *testing.T) {
	tr := NewTracker()
	fp := Fingerprint{Path: "/a", ContentSHA256: "c1"}
	if _, existed := tr.Observe(fp); existed {
		t.Fatal("first observation should not report an existing fingerprint")
	}
	fp.ContentSHA256 = "c2"
	change, existed := tr.Observe(fp)
	if !existed || !change.ContentReplaced {
		t.Fatalf("replacement not detected: existed=%v change=%+v", existed, change)
	}
}
