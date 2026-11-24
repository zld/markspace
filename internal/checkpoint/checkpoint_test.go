package checkpoint

import (
	"errors"
	"path/filepath"
	"testing"

	"archive/internal/fault"
	"archive/internal/remux"
)

func inputs() []InputDigest {
	return []InputDigest{
		{Name: "a.mxf", Format: "mxf", SHA256: "1111"},
		{Name: "b.mp4", Format: "mp4", SHA256: "2222"},
	}
}

func TestLoadMissingIsFresh(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "nope.ckpt")).Load()
	if err != nil || st != nil {
		t.Fatalf("st=%v err=%v", st, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "run.ckpt")
	store := New(path)
	st := &State{Inputs: inputs(), InputDigest: Digest(inputs())}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != Version || got.InputDigest != st.InputDigest || len(got.Inputs) != 2 {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestValidateInputsRejectsChangedDigest(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "c.ckpt"))
	st := &State{Inputs: inputs()}
	if err := store.ValidateInputs(st, inputs()); err != nil {
		t.Fatalf("identical inputs rejected: %v", err)
	}
	changed := inputs()
	changed[1].SHA256 = "3333"
	err := store.ValidateInputs(st, changed)
	if err == nil {
		t.Fatal("changed digest accepted")
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.CheckpointMismatch {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateInputsRejectsReorderingOfIdentity(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "c.ckpt"))
	st := &State{Inputs: inputs()}
	swapped := []InputDigest{
		{Name: "a.mxf", Format: "mp4", SHA256: "1111"},
		{Name: "b.mp4", Format: "mp4", SHA256: "2222"},
	}
	if err := store.ValidateInputs(st, swapped); err == nil {
		t.Fatal("format change accepted")
	}
}

func TestRecordCommitUpdatesOffsets(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "c.ckpt"))
	st := &State{Inputs: inputs()}
	tr := remux.TrackResult{Key: "mp4/t1", Logical: "video"}
	cf := remux.CommittedFragment{TrackKey: "mp4/t1", Seq: 0, Offset: 100, Size: 40}
	if err := store.RecordCommit(st, tr, cf); err != nil {
		t.Fatal(err)
	}
	cf2 := remux.CommittedFragment{TrackKey: "mp4/t1", Seq: 1, Offset: 140, Size: 50}
	if err := store.RecordCommit(st, tr, cf2); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := loaded.ProgressFor("mp4/t1", "video")
	if len(p.Fragments) != 2 || p.OutputOffset != 190 {
		t.Fatalf("progress = %+v", p)
	}
	committed := loaded.Committed()
	if len(committed["mp4/t1"]) != 2 {
		t.Fatalf("committed = %v", committed)
	}
}

func TestDigestIsOrderIndependentAndContentSensitive(t *testing.T) {
	a := inputs()
	b := []InputDigest{a[1], a[0]}
	if Digest(a) != Digest(b) {
		t.Fatal("digest depends on input order")
	}
	c := inputs()
	c[0].SHA256 = "changed"
	if Digest(a) == Digest(c) {
		t.Fatal("digest ignored content")
	}
}

func TestVersionMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.ckpt")
	if err := writeFile(path, `{"version":99}`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path).Load(); err == nil {
		t.Fatal("version mismatch accepted")
	}
}

func TestCorruptCheckpointIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.ckpt")
	if err := writeFile(path, `{not json`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path).Load(); err == nil {
		t.Fatal("unreadable checkpoint accepted")
	}
}

func TestAtomicSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	store := New(filepath.Join(dir, "c.ckpt"))
	if err := store.Save(&State{Inputs: inputs()}); err != nil {
		t.Fatal(err)
	}
	entries, err := readDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != "c.ckpt" {
		t.Fatalf("directory = %v", entries)
	}
}
