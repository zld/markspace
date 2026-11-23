package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyDependsOnAllInputs(t *testing.T) {
	base := Key([]byte("payload"), "24/1", "gop=48")
	if Key([]byte("payload2"), "24/1", "gop=48") == base {
		t.Fatal("input bytes must change the key")
	}
	if Key([]byte("payload"), "25/1", "gop=48") == base {
		t.Fatal("timebase must change the key")
	}
	if Key([]byte("payload"), "24/1", "gop=96") == base {
		t.Fatal("params must change the key")
	}
}

func TestPutGetHit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := Key([]byte("in"), "24/1", "p")
	if err := s.Put(k, "asset-a", []byte("result")); err != nil {
		t.Fatal(err)
	}
	got, hit, tampered, err := s.Get(k, "asset-a")
	if err != nil || !hit || tampered {
		t.Fatalf("hit=%v tampered=%v err=%v", hit, tampered, err)
	}
	if string(got) != "result" {
		t.Fatalf("payload %q", got)
	}
	if s.Hits != 1 {
		t.Fatalf("hits=%d", s.Hits)
	}
}

func TestTamperedDigestQuarantined(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	k := Key([]byte("in"), "24/1", "p")
	if err := s.Put(k, "asset-a", []byte("result")); err != nil {
		t.Fatal(err)
	}
	// Corrupt the payload on disk.
	if err := os.WriteFile(filepath.Join(dir, k+".bin"), []byte("forged"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, hit, tampered, err := s.Get(k, "asset-a")
	if err != nil || hit || !tampered {
		t.Fatalf("hit=%v tampered=%v err=%v", hit, tampered, err)
	}
	if s.Tampered != 1 || len(s.Quaranted) != 1 {
		t.Fatalf("tampered=%d quarantined=%v", s.Tampered, s.Quaranted)
	}
	// Quarantined entry must not be served again.
	if _, err := os.Stat(filepath.Join(dir, k+".bin")); !os.IsNotExist(err) {
		t.Fatal("payload should have been quarantined")
	}
	if _, err := os.Stat(filepath.Join(dir, "quarantine", k+".bin")); err != nil {
		t.Fatal("quarantine copy missing")
	}
}

func TestWrongOriginRejected(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := Key([]byte("in"), "24/1", "p")
	if err := s.Put(k, "asset-a", []byte("result")); err != nil {
		t.Fatal(err)
	}
	if _, hit, tampered, _ := s.Get(k, "asset-b"); hit || !tampered {
		t.Fatalf("foreign origin must be rejected: hit=%v tampered=%v", hit, tampered)
	}
}
