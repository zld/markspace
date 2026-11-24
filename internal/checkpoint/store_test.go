package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T, maxRecords int) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir(), maxRecords)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func rec(idx int64, digest string) Record {
	return Record{Index: idx, EventSeq: idx, InputByte: idx * 100, Watermark: idx * 1000, OutputDigest: digest}
}

func TestAppendAndLoad(t *testing.T) {
	s := newStore(t, 10)
	if _, err := s.Append(rec(1, "aaa")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(rec(2, "bbb")); err != nil {
		t.Fatal(err)
	}
	res, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.Records != 2 || len(res.All) != 2 {
		t.Fatalf("records = %d valid = %d", res.Records, len(res.All))
	}
	if res.Valid.Index != 2 {
		t.Errorf("valid = %d, want 2", res.Valid.Index)
	}
	if res.Corrupt != 0 || res.FallbackReason != "" {
		t.Errorf("unexpected corruption: %+v", res)
	}
}

func TestLoadMissingLog(t *testing.T) {
	s := newStore(t, 4)
	_, err := s.Load()
	if err == nil {
		t.Fatal("expected missing-log error")
	}
	if !strings.Contains(err.Error(), "no checkpoint log") {
		t.Errorf("error = %v", err)
	}
}

func TestLoadDetectsDigestMismatch(t *testing.T) {
	s := newStore(t, 10)
	s.Append(rec(1, "aaa"))
	path := filepath.Join(s.Dir(), "checkpoints.jsonl")
	data, _ := os.ReadFile(path)
	// Tamper with the watermark field of the last record without recomputing
	// its digest.
	tampered := strings.Replace(string(data), `"watermark_ns":1000`, `"watermark_ns":9999`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.Load()
	if err == nil {
		t.Fatal("expected corruption to be reported")
	}
	if res.Corrupt != 1 {
		t.Errorf("corrupt = %d, want 1", res.Corrupt)
	}
	if !strings.Contains(res.FallbackReason, "digest mismatch") {
		t.Errorf("fallback reason = %q", res.FallbackReason)
	}
	if !strings.Contains(res.FallbackReason, "byte ") {
		t.Errorf("fallback reason must locate the damage: %q", res.FallbackReason)
	}
}

func TestLoadFallsBackToPreviousValid(t *testing.T) {
	s := newStore(t, 10)
	s.Append(rec(1, "aaa"))
	s.Append(rec(2, "bbb"))
	path := filepath.Join(s.Dir(), "checkpoints.jsonl")
	data, _ := os.ReadFile(path)
	lines := strings.SplitAfter(string(data), "\n")
	// Corrupt the final record's digest field itself.
	last := strings.Replace(lines[1], `"index":2`, `"index":3`, 1)
	if err := os.WriteFile(path, []byte(lines[0]+last), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid == nil || res.Valid.Index != 1 {
		t.Fatalf("did not fall back: %+v", res.Valid)
	}
	if res.Corrupt != 1 {
		t.Errorf("corrupt = %d", res.Corrupt)
	}
	if !strings.Contains(res.FallbackReason, "byte ") {
		t.Errorf("fallback must state where: %q", res.FallbackReason)
	}
}

func TestLoadTruncatedLine(t *testing.T) {
	s := newStore(t, 10)
	s.Append(rec(1, "aaa"))
	path := filepath.Join(s.Dir(), "checkpoints.jsonl")
	data, _ := os.ReadFile(path)
	// Drop the trailing newline and half the record.
	if err := os.WriteFile(path, data[:len(data)-10], 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.Load()
	if err == nil {
		t.Fatal("expected no valid records")
	}
	if !strings.Contains(res.FallbackReason, "truncated record") {
		t.Errorf("reason = %q", res.FallbackReason)
	}
}

func TestCandidatesNewestFirst(t *testing.T) {
	s := newStore(t, 10)
	s.Append(rec(1, "a"))
	s.Append(rec(2, "b"))
	res, _ := s.Load()
	c := res.Candidates()
	if len(c) != 2 || c[0].Index != 2 || c[1].Index != 1 {
		t.Errorf("candidates = %+v", c)
	}
}

func TestCompactKeepsNewest(t *testing.T) {
	s := newStore(t, 2)
	for i := int64(1); i <= 5; i++ {
		s.Append(rec(i, "d"))
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	res, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if res.Records != 2 {
		t.Errorf("records after compact = %d, want 2", res.Records)
	}
	if res.Valid.Index != 5 {
		t.Errorf("newest = %d, want 5", res.Valid.Index)
	}
}

func TestNewStoreValidation(t *testing.T) {
	if _, err := NewStore("", 4); err == nil {
		t.Error("empty dir must be rejected")
	}
	if _, err := NewStore(t.TempDir(), 0); err == nil {
		t.Error("zero max records must be rejected")
	}
}

func TestRemove(t *testing.T) {
	s := newStore(t, 4)
	s.Append(rec(1, "a"))
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Error("load after remove should fail")
	}
}

func TestDigestCoversFields(t *testing.T) {
	base := rec(1, "a")
	variants := []Record{base, base, base, base}
	variants[1].Watermark++
	variants[2].OutputDigest = "b"
	variants[3].InputByte++
	seen := map[string]bool{}
	for _, v := range variants {
		d := v.digest()
		if seen[d] {
			t.Errorf("digest collision for a changed field: %+v", v)
		}
		seen[d] = true
	}
}
