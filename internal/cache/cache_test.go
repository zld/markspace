package cache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/example/mediad/internal/model"
)

func sampleSpec(id string) Spec {
	return Spec{
		SourceID:       id,
		InputDigest:    "abc",
		Codec:          "opus",
		SourceRate:     48000,
		SourceChannels: 2,
		TargetRate:     48000,
		Layout:         "stereo",
		LUFS:           -16,
		TruePeak:       -1.0,
	}
}

func TestKeyDeterministicAndSensitive(t *testing.T) {
	s := sampleSpec("src-1")
	k1 := Key(s)
	k2 := Key(s)
	if k1 != k2 || len(k1) != 64 {
		t.Fatalf("key not stable: %q", k1)
	}
	s2 := s
	s2.TargetRate = 24000
	if Key(s2) == k1 {
		t.Fatal("key must change when a processing spec changes")
	}
	s3 := s
	s3.SourceID = "src-2"
	if Key(s3) == k1 {
		t.Fatal("key must change when source id changes")
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := sampleSpec("src-1")
	key := Key(s)
	payload := []byte{1, 2, 3, 4}
	e := Entry{
		Spec:          s,
		SourceDigest:  "src-digest",
		ContentDigest: DigestPayload(payload),
		Payload:       payload,
	}
	if err := c.Put(key, e); err != nil {
		t.Fatal(err)
	}
	// No stray temp files left behind.
	if matches, _ := filepath.Glob(filepath.Join(dir, "tmp-*")); len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
	got, err := c.Get(key, "src-digest", e.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || string(got.Payload) != string(payload) {
		t.Fatalf("get returned %+v", got)
	}
}

func TestGetMissReturnsNil(t *testing.T) {
	c, _ := New(t.TempDir())
	got, err := c.Get("nonexistent", "x", "y")
	if err != nil || got != nil {
		t.Fatalf("missing key should be (nil,nil), got %v %v", got, err)
	}
}

func TestDigestMismatchInvalidates(t *testing.T) {
	c, _ := New(t.TempDir())
	s := sampleSpec("src-1")
	key := Key(s)
	if err := c.Put(key, Entry{
		Spec: s, SourceDigest: "old-src", ContentDigest: "old-content",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := c.Get(key, "new-src", "new-content")
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeCacheDigestMismatch {
		t.Fatalf("expected digest mismatch error, got %v", err)
	}
	if err := c.Invalidate(key); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get(key, "new-src", "new-content"); got != nil {
		t.Fatal("entry should be gone after invalidate")
	}
}

func TestAtomicWriteLeavesNoPartial(t *testing.T) {
	dir := t.TempDir()
	c, _ := New(dir)
	key := Key(sampleSpec("s"))
	if err := c.Put(key, Entry{Spec: sampleSpec("s"), SourceDigest: "d", ContentDigest: "c"}); err != nil {
		t.Fatal(err)
	}
	// The final file is fully present and readable as JSON.
	info, err := os.Stat(filepath.Join(dir, key+".json"))
	if err != nil || info.Size() == 0 {
		t.Fatalf("final cache file missing/empty: %v", err)
	}
}
