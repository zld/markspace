package replay

import (
	"testing"
	"time"

	"mediakey/internal/manifest"
)

func TestReplayDuplicateAndSequence(t *testing.T) {
	c, err := New(8, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_000_000, 0)
	if err := c.Check("n1", 1, base); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if code := manifest.ErrorCode(c.Check("n1", 2, base)); code != manifest.ReasonReplay {
		t.Fatalf("duplicate nonce = %s, want replay", code)
	}
	if code := manifest.ErrorCode(c.Check("n1", 0, base)); code != manifest.ReasonOutOfOrder {
		t.Fatalf("regressed seq = %s, want sequence_out_of_order", code)
	}
	if code := manifest.ErrorCode(c.Check("n2", 1, base)); code != manifest.ReasonOutOfOrder {
		t.Fatalf("stale global seq = %s, want sequence_out_of_order", code)
	}
	if err := c.Check("n2", 2, base); err != nil {
		t.Fatalf("fresh nonce with advancing seq: %v", err)
	}
}

func TestEvictionTombstoneNeverAutoAllows(t *testing.T) {
	c, err := New(2, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_000_000, 0)
	// Fill capacity; inserting n3 evicts the oldest (n1) into a tombstone.
	if err := c.Check("n1", 1, base); err != nil {
		t.Fatal(err)
	}
	if err := c.Check("n2", 2, base); err != nil {
		t.Fatal(err)
	}
	if err := c.Check("n3", 3, base); err != nil {
		t.Fatal(err)
	}
	live, dead := c.Size()
	if live != 2 || dead != 1 {
		t.Fatalf("sizes live=%d dead=%d, want 2/1", live, dead)
	}
	// The evicted nonce must NOT be silently re-accepted inside the window.
	if code := manifest.ErrorCode(c.Check("n1", 4, base.Add(10*time.Second))); code != manifest.ReasonWindowOutside {
		t.Fatalf("evicted replay = %s, want window_outside", code)
	}
	// After the window closes, the same nonce is legitimately reusable.
	if err := c.Check("n1", 5, base.Add(120*time.Second)); err != nil {
		t.Fatalf("post-window reuse should pass: %v", err)
	}
}

func TestCacheLimits(t *testing.T) {
	if _, err := New(0, time.Minute); err == nil {
		t.Fatal("zero capacity must be rejected")
	}
	if _, err := New(manifest.MaxCacheEntries+1, time.Minute); err == nil {
		t.Fatal("over-limit capacity must be rejected")
	}
	if _, err := New(4, 0); err == nil {
		t.Fatal("non-positive window must be rejected")
	}
}
