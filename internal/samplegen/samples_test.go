package samplegen

import "testing"

func TestAllDemoSamplesAreStableAndIndexed(t *testing.T) {
	a := All(17, 1<<20)
	b := All(17, 1<<20)
	if len(a) != 13 || len(b) != 13 {
		t.Fatalf("sample counts %d/%d, want 13", len(a), len(b))
	}
	seenIDs := make(map[string]bool)
	for i := range a {
		if a[i].Index != i || b[i].Index != i {
			t.Fatalf("sample %d has indices %d/%d", i, a[i].Index, b[i].Index)
		}
		if a[i].ID != b[i].ID || a[i].InputName != b[i].InputName {
			t.Fatalf("sample %d is not deterministic", i)
		}
		if seenIDs[a[i].ID] {
			t.Fatalf("duplicate sample ID %q", a[i].ID)
		}
		seenIDs[a[i].ID] = true
	}
}
