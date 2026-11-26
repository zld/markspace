package cache

import (
	"bytes"
	"testing"
)

func TestDerivationKeyIncludesSourceAndConfig(t *testing.T) {
	k1 := DerivationKey("src-a", "cfg-1", "seg-1")
	if k1 == DerivationKey("src-b", "cfg-1", "seg-1") {
		t.Fatal("source digest did not change key")
	}
	if k1 == DerivationKey("src-a", "cfg-2", "seg-1") {
		t.Fatal("config digest did not change key")
	}
	if k1 != DerivationKey("src-a", "cfg-1", "seg-1") {
		t.Fatal("key not stable")
	}
}

func TestGraphInvalidatesOnlyDependentBranch(t *testing.T) {
	g := NewGraph()
	mustAdd := func(n Node) Node {
		got, err := g.Add(n)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	mustAdd(Node{ID: "frag-a1", TrackID: "A", SourceSHA: "s-a1", ConfigSHA: "c", DependsOn: []string{"source-a1"}})
	mustAdd(Node{ID: "frag-a2", TrackID: "A", SourceSHA: "s-a2", ConfigSHA: "c", DependsOn: []string{"source-a2"}})
	mustAdd(Node{ID: "frag-b1", TrackID: "B", SourceSHA: "s-b1", ConfigSHA: "c", DependsOn: []string{"source-b1"}})
	affected := g.Affected("source-a1")
	if len(affected) != 1 || affected[0] != "frag-a1" {
		t.Fatalf("affected=%v want only frag-a1", affected)
	}
}

func TestStoreAtomicRoundTripAndHit(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := DerivationKey("src", "cfg", "id")
	if _, hit, err := s.Get(key); err != nil || hit {
		t.Fatalf("initial hit=%v err=%v", hit, err)
	}
	path, err := s.Put(key, []byte("fragment"))
	if err != nil {
		t.Fatal(err)
	}
	got, hit, err := s.Get(key)
	if err != nil || !hit || !bytes.Equal(got, []byte("fragment")) {
		t.Fatalf("cached=%q hit=%v err=%v", got, hit, err)
	}
	if path != s.Path(key) {
		t.Fatalf("path=%s want %s", path, s.Path(key))
	}
}
