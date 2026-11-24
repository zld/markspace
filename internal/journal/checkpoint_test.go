package journal_test

import (
	"path/filepath"
	"testing"

	"mediad/internal/journal"
)

func sampleDigest(t *testing.T) journal.ConfigDigest {
	t.Helper()
	return journal.ConfigDigest{
		ManifestSHA:    "abc123",
		ParserVersions: map[string]string{"mp4": "mp4-v1", "rtp": "rtp-v1"},
		Parameters:     map[string]string{"target_duration_ns": "2000000000"},
	}
}

func TestDigestDeterministic(t *testing.T) {
	d1, err := sampleDigest(t).Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Same content, different map insertion order.
	d2cfg := journal.ConfigDigest{
		ManifestSHA:    "abc123",
		ParserVersions: map[string]string{"rtp": "rtp-v1", "mp4": "mp4-v1"},
		Parameters:     map[string]string{"target_duration_ns": "2000000000"},
	}
	d2, _ := d2cfg.Digest()
	if d1 != d2 {
		t.Fatalf("digest must be order-independent: %s vs %s", d1, d2)
	}
	d3cfg := sampleDigest(t)
	d3cfg.Parameters["target_duration_ns"] = "1000000000"
	d3, _ := d3cfg.Digest()
	if d3 == d1 {
		t.Fatal("different parameters must produce a different digest")
	}
}

func TestCommitAndResume(t *testing.T) {
	dir := t.TempDir()
	digest, _ := sampleDigest(t).Digest()
	j := journal.Open(dir, digest)
	if err := j.Ensure(); err != nil {
		t.Fatal(err)
	}
	committed, err := j.IsCommitted("case-1")
	if err != nil || committed {
		t.Fatal("fresh state must have no committed item")
	}
	if err := j.Commit(journal.Item{CaseID: "case-1", Result: []byte(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	// Reopen as a brand new journal instance (simulated process restart).
	j2 := journal.Open(dir, digest)
	committed, err = j2.IsCommitted("case-1")
	if err != nil || !committed {
		t.Fatal("committed item must survive restart")
	}
	// Overwriting a committed result is refused.
	err = j2.Commit(journal.Item{CaseID: "case-1", Result: []byte(`{}`)})
	if err == nil {
		t.Fatal("overwriting committed result must fail")
	}
}

func TestFailureIsRetryable(t *testing.T) {
	dir := t.TempDir()
	digest, _ := sampleDigest(t).Digest()
	j := journal.Open(dir, digest)
	errs := []journal.SourcedError{{Kind: "corrupt_box", Msg: "bad", Loc: "x#1"}}
	if err := j.RecordFailure("case-x", errs, 1); err != nil {
		t.Fatal(err)
	}
	st, item, err := j.Status("case-x")
	if err != nil || st != journal.StatusFailed {
		t.Fatalf("status = %s, %v", st, err)
	}
	if len(item.Errors) != 1 || item.Attempts != 1 {
		t.Fatalf("failure record wrong: %+v", item)
	}
	committed, _ := j.IsCommitted("case-x")
	if committed {
		t.Fatal("failed item must not count as committed")
	}
	// A later success can replace the failure with a committed result.
	if err := j.Commit(journal.Item{CaseID: "case-x", Result: []byte(`{}`)}); err != nil {
		t.Fatalf("retry after failure must be allowed: %v", err)
	}
	committed, _ = j.IsCommitted("case-x")
	if !committed {
		t.Fatal("retry did not commit")
	}
}

func TestListOrdered(t *testing.T) {
	dir := t.TempDir()
	digest, _ := sampleDigest(t).Digest()
	j := journal.Open(dir, digest)
	for _, id := range []string{"b", "a", "c"} {
		if err := j.Commit(journal.Item{CaseID: id}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := j.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].CaseID != "a" || items[2].CaseID != "c" {
		t.Fatalf("list not ordered: %+v", items)
	}
}

func TestDigestIsolation(t *testing.T) {
	dir := t.TempDir()
	d1, _ := sampleDigest(t).Digest()
	cfg2 := sampleDigest(t)
	cfg2.ManifestSHA = "different"
	d2, _ := cfg2.Digest()
	j1 := journal.Open(dir, d1)
	_ = j1.Commit(journal.Item{CaseID: "x"})
	j2 := journal.Open(dir, d2)
	committed, _ := j2.IsCommitted("x")
	if committed {
		t.Fatal("state must not leak across configuration digests")
	}
	if filepath.Join(dir, d1) == filepath.Join(dir, d2) {
		t.Fatal("digest dirs collide")
	}
}
