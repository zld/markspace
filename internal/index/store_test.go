package index

import (
	"errors"
	"testing"
)

func sampleRows(key string) []Row {
	return []Row{
		{SampleIndex: 0, ContentKey: key, Offset: 0, Length: 4},
		{SampleIndex: 1, ContentKey: key, Offset: 4, Length: 3},
	}
}

func TestBuildLookupAndRebuild(t *testing.T) {
	s := NewStore()
	rows := sampleRows("content-key")
	s.Build("container-a", "meta-a", rows)

	row, err := s.Lookup("container-a", "meta-a", 1)
	if err != nil || row.Offset != 4 {
		t.Fatalf("lookup failed: %+v %v", row, err)
	}
	// A caller trusting a stale identity is rejected, never silently
	// served mappings from a different container.
	if _, err := s.Lookup("container-b", "meta-a", 1); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("want ErrIdentityMismatch, got %v", err)
	}

	// Rebuild with a new metadata identity: mappings replaced, one
	// invalidation and a second build counted.
	s.Build("container-a", "meta-b", sampleRows("content-key"))
	if st := s.Stats(); st.Builds != 2 || st.Invalidations != 1 || st.Lookups != 2 {
		t.Fatalf("rebuild stats wrong: %+v", st)
	}
	if _, err := s.Lookup("container-a", "meta-b", 0); err != nil {
		t.Fatalf("post-rebuild lookup failed: %v", err)
	}
}

func TestInvalidateClearsIdentity(t *testing.T) {
	s := NewStore()
	s.Build("c", "m", sampleRows("k"))
	s.Invalidate()
	if c, m := s.Identity(); c != "" || m != "" || len(s.Rows()) != 0 {
		t.Fatal("invalidate must clear identity and rows")
	}
	if st := s.Stats(); st.Invalidations != 1 {
		t.Fatalf("invalidation not counted: %+v", st)
	}
}

func TestFingerprintStable(t *testing.T) {
	rows := sampleRows("k")
	a := Fingerprint("c", "m", rows)
	b := Fingerprint("c", "m", sampleRows("k"))
	if a != b {
		t.Fatal("identical identity+rows must fingerprint equally")
	}
	if Fingerprint("c2", "m", rows) == a {
		t.Fatal("container change must change fingerprint")
	}
}
