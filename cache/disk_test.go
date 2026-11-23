package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiskStoreVerifiesAcrossProcessRestart(t *testing.T) {
	dir := t.TempDir()
	data := []byte("disk-backed-segment")
	meta := []byte(`{"index":0}`)
	p := prov("edge-a", "seg-0", 0, int64(len(data)))

	first, err := NewDiskStore(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.PutWithMeta(data, p, meta); err != nil {
		t.Fatal(err)
	}

	// A new store over the same directory rehydrates provenance from disk.
	second, err := NewDiskStore(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	expect := p
	expect.MetaDigest = Key(meta)
	res := second.Lookup(Key(data), expect)
	if !res.Hit || string(res.Data) != string(data) || string(res.Entry.Meta) != string(meta) {
		t.Fatalf("res=%+v", res)
	}
}

func TestDiskStoreRejectsForgedNodeAndRemovesBody(t *testing.T) {
	dir := t.TempDir()
	data := []byte("payload-for-disk")
	p := prov("edge-a", "seg-0", 0, int64(len(data)))
	s, err := NewDiskStore(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(data, p); err != nil {
		t.Fatal(err)
	}
	res := s.Lookup(Key(data), prov("edge-z", "seg-0", 0, int64(len(data))))
	if res.Hit || res.Rejection == nil {
		t.Fatalf("res=%+v", res)
	}
	if _, err := os.Stat(s.objectPath(Key(data))); !os.IsNotExist(err) {
		t.Fatal("poisoned body must be removed from the store")
	}
	if got := s.Quarantined(Key(data)); len(got) != 1 {
		t.Fatalf("quarantine=%d", len(got))
	}
}

func TestDiskStorePersistsProvenanceBesideBody(t *testing.T) {
	dir := t.TempDir()
	data := []byte("abc")
	s, _ := NewDiskStore(dir, 1<<20)
	if _, err := s.Put(data, prov("edge-a", "s", 0, 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "objects", Key(data)[:2], Key(data)+".prov.json")); err != nil {
		t.Fatalf("provenance not persisted: %v", err)
	}
}
