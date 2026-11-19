package keys

import (
	"testing"

	"mediakey/internal/manifest"
)

func testRoot(t *testing.T) []byte {
	t.Helper()
	return DeriveTestRootKey([]byte("unit-test-seed"))
}

func TestRotationCountOnlyCountsNewRounds(t *testing.T) {
	d, err := New(testRoot(t), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.RotationCount() != 0 || d.ActiveRound() != 0 {
		t.Fatal("new dispatcher must start at round 0 with zero rotations")
	}
	// Re-activating already-active rounds is a no-op and never counts.
	for _, idx := range []int{0, 1, 2} {
		if _, err := d.EnsureForIndex(idx); err != nil {
			t.Fatal(err)
		}
	}
	if d.RotationCount() != 0 {
		t.Fatalf("count = %d, want 0 before first new round", d.RotationCount())
	}
	if r, _ := d.EnsureForIndex(3); r != 1 {
		t.Fatalf("round for idx 3 = %d, want 1", r)
	}
	if d.RotationCount() != 1 || d.ActiveRound() != 1 {
		t.Fatalf("state = count %d round %d, want 1/1", d.RotationCount(), d.ActiveRound())
	}
	// Jump ahead activates each intermediate round exactly once.
	if _, err := d.EnsureForIndex(9); err != nil {
		t.Fatal(err)
	}
	if d.RotationCount() != 3 {
		t.Fatalf("count = %d, want 3", d.RotationCount())
	}
}

func TestUnknownRevokedAndClosedRounds(t *testing.T) {
	d, _ := New(testRoot(t), 2)
	if _, _, err := d.KeyForRound(7); manifest.ErrorCode(err) != manifest.ReasonUnknownRound {
		t.Fatalf("unknown round code = %v", err)
	}
	if _, err := d.EnsureForIndex(2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.KeyForRound(1); err != nil {
		t.Fatalf("activated round should serve: %v", err)
	}
	if err := d.Revoke(1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.KeyForRound(1); manifest.ErrorCode(err) != manifest.ReasonRevokedRound {
		t.Fatalf("revoked code = %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.KeyForRound(0); manifest.ErrorCode(err) != manifest.ReasonDispatcherClosed {
		t.Fatalf("closed code = %v", err)
	}
	if _, err := d.EnsureForIndex(4); manifest.ErrorCode(err) != manifest.ReasonDispatcherClosed {
		t.Fatalf("closed rotate code = %v", err)
	}
}

func TestDispatcherValidation(t *testing.T) {
	if _, err := New([]byte("short"), 2); err == nil {
		t.Fatal("short root key must be rejected")
	}
	if _, err := New(testRoot(t), 0); err == nil {
		t.Fatal("non-positive rotate interval must be rejected")
	}
}
