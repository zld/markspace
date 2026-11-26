package journal

import (
	"path/filepath"
	"testing"
)

func TestDeliveryKeyStableAndIntervalSensitive(t *testing.T) {
	a := DeliveryKey("evt-1", 500, 1500)
	b := DeliveryKey("evt-1", 500, 1500)
	c := DeliveryKey("evt-1", 500, 1501)
	d := DeliveryKey("evt-2", 500, 1500)
	if len(a) != 64 || a != b {
		t.Fatalf("stable key failed: %s/%s", a, b)
	}
	if a == c || a == d {
		t.Fatal("key must change with event ID or interval")
	}
}

func TestAppendLoadAndManifest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := DeliveryKey("evt", 0, 10)
	rec := Record{Key: key, EventID: "evt", StartMS: 0, EndMS: 10, Artifact: key + ".mp4", SHA256: "x"}
	if err := s.AppendRecord(rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got[key].SHA256 != "x" {
		t.Fatalf("loaded %#v", got[key])
	}
	m := Manifest{DeliveryKey: key, SHA256: "x"}
	if err := s.WriteManifest(m); err != nil {
		t.Fatal(err)
	}
	read, err := ReadManifest(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if read.DeliveryKey != key {
		t.Fatalf("manifest key=%s", read.DeliveryKey)
	}
	if filepath.Base(s.ManifestPath()) != ManifestName {
		t.Fatal("bad manifest path")
	}
}

func TestFinalPathRejectsUnsafeKey(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinalPath("../escape"); err == nil {
		t.Fatal("unsafe key accepted")
	}
}
