package fixture

import (
	"testing"

	"mediakey/internal/manifest"
)

func TestDeterministicContent(t *testing.T) {
	p := Plan{AssetID: "a", Chunks: 4, ChunkSize: 100, RotateEvery: 2}
	s1, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(s1.Plaintext) != string(s2.Plaintext) {
		t.Fatal("generated content must be deterministic")
	}
	if len(s1.Plaintext) != 400 {
		t.Fatalf("plaintext length = %d, want 400", len(s1.Plaintext))
	}
	wantRounds := []int{0, 0, 1, 1}
	for i, r := range s1.ChunkRounds {
		if r != wantRounds[i] {
			t.Fatalf("round[%d] = %d, want %d", i, r, wantRounds[i])
		}
	}
}

func TestFaultPlacementIsDistinct(t *testing.T) {
	sc, err := New(Plan{
		AssetID: "a", Chunks: 12, ChunkSize: 32, RotateEvery: 3,
		Injections: map[string]bool{
			InjectExpired: true, InjectReplay: true, InjectTamper: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sc.PlaceFaults()
	if len(sc.Faults) != 3 {
		t.Fatalf("placed %d faults, want 3 distinct chunks", len(sc.Faults))
	}
	seen := map[string]bool{}
	for _, name := range sc.Faults {
		if seen[name] {
			t.Fatalf("fault %s placed twice", name)
		}
		seen[name] = true
	}
}

func TestParseInjections(t *testing.T) {
	got, err := ParseInjections(" expired ,replay,tamper ")
	if err != nil {
		t.Fatal(err)
	}
	if !got[InjectExpired] || !got[InjectReplay] || !got[InjectTamper] {
		t.Fatalf("normalization failed: %+v", got)
	}
	if _, err := ParseInjections("nope"); manifest.ErrorCode(err) != manifest.ReasonInvalidInput {
		t.Fatalf("unknown injection code = %v", err)
	}
	if empty, err := ParseInjections(""); err != nil || len(empty) != 0 {
		t.Fatal("empty injection list should be valid")
	}
}

func TestPlanLimits(t *testing.T) {
	if _, err := New(Plan{Chunks: 0, ChunkSize: 32, RotateEvery: 1}); err == nil {
		t.Fatal("zero chunks must be rejected")
	}
	if _, err := New(Plan{Chunks: 4, ChunkSize: 32, RotateEvery: 0}); err == nil {
		t.Fatal("zero rotation interval must be rejected")
	}
}
