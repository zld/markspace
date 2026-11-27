package derive

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
)

func TestAddressDeterminismAndSensitivity(t *testing.T) {
	p := Params{Kind: "mp4", TargetBPS: 1_000_000, Version: 1}
	a1, err := AddressFor("aabbccdd", p)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := AddressFor("aabbccdd", p)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Key != a2.Key || len(a1.Key) != 64 {
		t.Fatal("address must be stable and a 64-char hex key")
	}
	// Different input -> different key even with identical params.
	a3, err := AddressFor("00112233", p)
	if err != nil {
		t.Fatal(err)
	}
	if a3.Key == a1.Key {
		t.Fatal("different inputs must not share an address")
	}
	// Different params -> different key for the same input.
	a4, err := AddressFor("aabbccdd", Params{Kind: "mp4", TargetBPS: 2_000_000, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if a4.Key == a1.Key {
		t.Fatal("different parameters must not share an address")
	}
	if _, err := AddressFor("", p); err == nil {
		t.Fatal("empty input digest must be rejected")
	}
}

func TestStorePutGetAndIdempotent(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "derive"))
	if err != nil {
		t.Fatal(err)
	}
	addr, err := AddressFor("deadbeef", Params{Kind: "report", TargetBPS: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Has(addr); ok {
		t.Fatal("fresh store must not contain the address")
	}
	content := []byte("derivative-bytes")
	if err := s.Put(addr, content); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(addr)
	if err != nil || string(got) != string(content) {
		t.Fatalf("get mismatch: %q err=%v", got, err)
	}
	// Re-put identical content is a safe cache hit.
	if err := s.Put(addr, content); err != nil {
		t.Fatalf("idempotent put failed: %v", err)
	}
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("count: got %d want 1", n)
	}
}

func TestStoreCollisionDetected(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "derive"))
	addr, _ := AddressFor("cafe", Params{Kind: "mp4"})
	if err := s.Put(addr, []byte("first")); err != nil {
		t.Fatal(err)
	}
	err := s.Put(addr, []byte("second-different-payload"))
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrDigestMismatch {
		t.Fatalf("want collision DIGEST_MISMATCH, got %v", err)
	}
}

func TestStoreInvalidateScopedToInput(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "derive"))
	params := []Params{
		{Kind: "mp4", TargetBPS: 1_000_000},
		{Kind: "report", TargetBPS: 1_000_000},
	}
	addrA1, _ := AddressFor("input-A", params[0])
	addrA2, _ := AddressFor("input-A", params[1])
	addrB1, _ := AddressFor("input-B", params[0])
	for _, a := range []*Address{addrA1, addrA2, addrB1} {
		if err := s.Put(a, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.Invalidate("input-A", params)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("invalidate removed %d, want 2", removed)
	}
	if ok, _ := s.Has(addrA1); ok {
		t.Fatal("input-A derivative should be gone")
	}
	if ok, _ := s.Has(addrB1); !ok {
		t.Fatal("input-B derivative must survive source-scoped invalidation")
	}
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("remaining count: got %d want 1", n)
	}
	// Invalidating again is a no-op without error.
	if n, err := s.Invalidate("input-A", params); err != nil || n != 0 {
		t.Fatalf("re-invalidate: n=%d err=%v", n, err)
	}
}

func TestStoreShardedLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "derive")
	s, _ := NewStore(root)
	addr, _ := AddressFor("1234abcd", Params{Kind: "mp4"})
	if err := s.Put(addr, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, addr.Key[0:2], addr.Key[2:4], addr.Key)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("sharded path missing: %v", err)
	}
}
