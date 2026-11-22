package txn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func inputs(tag string) []InputDigest {
	return []InputDigest{{Name: "a.mp4", SHA256: "sum-" + tag}}
}

func TestCommitAndRecover(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCoordinator(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RecordInputs("parse", inputs("x")); err != nil {
		t.Fatal(err)
	}
	if err := c.Begin("parse"); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit("parse", inputs("x"), "out-1", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	c.Close()

	// Simulate crash: corrupt the WAL tail, then recover.
	walPath := filepath.Join(dir, "wal.log")
	f, _ := os.OpenFile(walPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"seq":99,"kind":"commit","stage":"parse","payload":{}}` + "\n")
	f.Close()

	c2, err := NewCoordinator(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	done, err := c2.Done("parse", inputs("x"))
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	// Corrupt record must have been truncated.
	st, _ := os.Stat(walPath)
	c3, _ := NewCoordinator(dir)
	defer c3.Close()
	recs, _ := c3.wal.Replay()
	for _, r := range recs {
		if r.Seq == 99 {
			t.Fatal("corrupt record survived recovery")
		}
	}
	_ = st
}

func TestRollbackOnValidationFailure(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewCoordinator(dir)
	defer c.Close()
	tmp := filepath.Join(dir, "tmp-segment.bin")
	if err := os.WriteFile(tmp, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := c.Commit("merge", inputs("x"), "out", []string{tmp},
		func() error { return errors.New("checksum mismatch") })
	if err == nil {
		t.Fatal("want commit failure")
	}
	if c.Rollbacks != 1 {
		t.Fatalf("rollbacks = %d", c.Rollbacks)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("temp artifact not rolled back")
	}
	if _, ok := c.Committed("merge"); ok {
		t.Fatal("stage must not be committed")
	}
	// Retry after rollback succeeds.
	if err := c.Commit("merge", inputs("x"), "out", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Committed("merge"); !ok {
		t.Fatal("retry did not commit")
	}
}

func TestInputChangedRefusesReuse(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewCoordinator(dir)
	defer c.Close()
	if err := c.Commit("parse", inputs("v1"), "out", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	done, err := c.Done("parse", inputs("v2"))
	if done || !errors.Is(err, ErrInputChanged) {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if len(c.InputChanges) != 1 || c.InputChanges[0] != "parse" {
		t.Fatalf("input changes = %v", c.InputChanges)
	}
}

func TestTempPathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewCoordinator(dir)
	defer c.Close()
	outside := filepath.Join(filepath.Dir(dir), "escape.bin")
	err := c.Commit("merge", inputs("x"), "out", []string{outside},
		func() error { return errors.New("fail") })
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("err = %v", err)
	}
}

func TestProvenanceDeterministic(t *testing.T) {
	mk := func() *Provenance {
		p := NewProvenance(map[string]string{"seed": "17", "bitrate": "4000000"})
		p.AddInput("a.mp4", "sum-a")
		p.AddInput("b.mp4", "sum-b")
		p.AddDecision("sync", "correct-offset", "audio leads by 10ms")
		return p
	}
	if mk().Digest() != mk().Digest() {
		t.Fatal("provenance digest not deterministic")
	}
	p2 := mk()
	p2.AddDecision("merge", "rollback", "validation failed")
	if p2.Digest() == mk().Digest() {
		t.Fatal("digest must change with decisions")
	}
}

func TestWALTornTail(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(KindBegin, "s1", nil)
	w.Append(KindCommit, "s1", CommitPayload{Output: "x"})
	// Torn write: partial JSON line.
	w.f.WriteString(`{"seq":3,"kind":"comm`)
	w.Close()

	w2, _ := OpenWAL(dir)
	defer w2.Close()
	recs, err := w2.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d", len(recs))
	}
	// Appends continue after the valid boundary.
	if err := w2.Append(KindBegin, "s2", nil); err != nil {
		t.Fatal(err)
	}
	recs, _ = w2.Replay()
	if len(recs) != 3 || recs[2].Stage != "s2" {
		t.Fatalf("records = %+v", recs)
	}
}
