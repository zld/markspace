package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

type state struct {
	Count int    `json:"count"`
	Hash  string `json:"hash"`
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Completed("ingest") {
		t.Fatal("stage should not be completed yet")
	}
	if err := s.Save("ingest", state{Count: 7, Hash: "abc"}); err != nil {
		t.Fatal(err)
	}
	if !s.Completed("ingest") {
		t.Fatal("stage should be completed")
	}
	var got state
	ok, err := s.Load("ingest", &got)
	if err != nil || !ok {
		t.Fatalf("load ok=%v err=%v", ok, err)
	}
	if got.Count != 7 || got.Hash != "abc" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadMissing(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	var got state
	ok, err := s.Load("nope", &got)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestNoTempFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	if err := s.Save("rtp", state{Count: 1}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestRejectsPathEscape(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if err := s.Save("../evil", state{}); err == nil {
		t.Fatal("expected error for path escape")
	}
	if err := s.Save("a/b", state{}); err == nil {
		t.Fatal("expected error for separator")
	}
}

func TestCorruptStateReported(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got state
	if _, err := s.Load("x", &got); err == nil {
		t.Fatal("expected corrupt-state error")
	}
}
