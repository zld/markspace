package cache_test

import (
	"testing"

	"assetcheck/internal/cache"
)

func fp(c, s, m string) cache.Fingerprints {
	return cache.Fingerprints{Content: c, Structure: s, Metadata: m}
}

func TestInvalidationPolicyMatrix(t *testing.T) {
	old := cache.Entry{Path: "a.wav", Fingerprints: fp("c1", "s1", "m1")}

	if inv := cache.Check(old, true, fp("c2", "s1", "m1")); len(inv) != 1 ||
		inv[0].Object != "parse-cache" || inv[0].Reason != "content-fingerprint-mismatch" {
		t.Fatalf("content policy wrong: %+v", inv)
	}
	if inv := cache.Check(old, true, fp("c1", "s2", "m1")); len(inv) != 1 ||
		inv[0].Object != "structure-cache" {
		t.Fatalf("structure must invalidate structure-cache only: %+v", inv)
	}
	if inv := cache.Check(old, true, fp("c1", "s1", "m2")); len(inv) != 1 ||
		inv[0].Object != "time-index" {
		t.Fatalf("metadata must invalidate time-index: %+v", inv)
	}
	if inv := cache.Check(old, true, fp("c1", "s1", "m1")); len(inv) != 0 {
		t.Fatalf("unchanged fingerprints must not invalidate: %+v", inv)
	}
	if inv := cache.Check(old, false, fp("c1", "s1", "m1")); inv != nil {
		t.Fatalf("cold cache must report no invalidation: %+v", inv)
	}
}

func TestStorePersistAndReload(t *testing.T) {
	path := t.TempDir() + "/cache.json"
	s, err := cache.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(cache.Entry{Path: "x.wav", Fingerprints: fp("a", "b", "c"), SummaryDigest: "d1"}); err != nil {
		t.Fatal(err)
	}
	s2, err := cache.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := s2.Get("x.wav")
	if !ok || e.SummaryDigest != "d1" || e.Fingerprints.Content != "a" {
		t.Fatalf("reloaded entry wrong: %+v ok=%v", e, ok)
	}
}
