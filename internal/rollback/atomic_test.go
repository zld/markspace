package rollback

import (
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/fault"
)

func publish(t *testing.T, s *Store, name, content string) {
	t.Helper()
	r, err := s.Begin(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Write("manifest.json", []byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := r.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := s.Switch(name); err != nil {
		t.Fatal(err)
	}
}

func TestPublishAndVerify(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	publish(t, s, "r1", `{"version":0}`)
	active, err := s.Active()
	if err != nil || active != "r1" {
		t.Fatalf("active %q err %v", active, err)
	}
	if err := s.Verify("r1"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	publish(t, s, "r1", `{"version":0}`)
	p := filepath.Join(s.Root, "releases", "r1", "manifest.json")
	if err := os.WriteFile(p, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify("r1"); !fault.HasCode(err, fault.ErrIntegrity) {
		t.Fatalf("got %v", err)
	}
}

func TestRecoverAfterInterruptedPublish(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	publish(t, s, "r1", `{"version":0}`)
	// crash mid-publish of r2: files written, no checksum record
	r2, err := s.Begin("r2")
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Write("manifest.json", []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Active != "r1" || !rep.ActiveIntact {
		t.Fatalf("report %+v", rep)
	}
	if len(rep.Removed) != 1 || rep.Removed[0] != "r2" {
		t.Fatalf("removed %v", rep.Removed)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "releases", "r2")); !os.IsNotExist(err) {
		t.Fatal("incomplete release must be removed")
	}
}

func TestRecoverAfterSwitchKeepsNew(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	publish(t, s, "r1", `{"version":0}`)
	publish(t, s, "r2", `{"version":1}`)
	rep, err := s.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Active != "r2" || !rep.ActiveIntact || len(rep.Removed) != 0 {
		t.Fatalf("report %+v", rep)
	}
}

func TestRecoverEmptyStore(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rep, err := s.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Active != "" || rep.ActiveIntact {
		t.Fatalf("report %+v", rep)
	}
}
