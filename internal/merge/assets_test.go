package merge

import (
	"os"
	"path/filepath"
	"testing"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

func entry(key, version string, unit int, payload []byte) Entry {
	return Entry{
		Asset: Asset{
			Key: key, Version: version, UnitIndex: unit,
			Offset: int64(unit * 10), Size: int64(len(payload)),
			TimeFrom: clock.MustRational(int64(unit), 1),
			TimeTo:   clock.MustRational(int64(unit+1), 1),
			Digest:   digestOf(payload),
		},
		Payload: payload,
	}
}

func TestBuildReconcilesIdenticalVersions(t *testing.T) {
	v1 := Version{Name: "v1", Entries: []Entry{
		entry("a", "v1", 0, []byte("alpha")), entry("b", "v1", 1, []byte("bravo")),
	}}
	v2 := Version{Name: "v2", Entries: []Entry{
		entry("a", "v2", 0, []byte("alpha")), entry("b", "v2", 1, []byte("bravo")),
	}}
	var rep fault.Report
	m, err := Build([]Version{v1, v2}, &rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Assets) != 2 || rep.Count() != 0 || len(m.Issues) != 0 {
		t.Fatalf("clean merge expected, got assets=%d issues=%v", len(m.Assets), m.Issues)
	}
	// Smallest version name wins deterministically.
	if m.Assets[0].Version != "v1" {
		t.Fatalf("winner = %q, want v1", m.Assets[0].Version)
	}
	if m.Digest != DigestManifest(m) || len(m.Digest) != 64 {
		t.Fatalf("bad digest %q", m.Digest)
	}
}

func TestBuildReportsMissingIndexAndByteConflicts(t *testing.T) {
	// v1 has a,b,c ; v2 has a (moved unit) and b (different bytes), no c.
	v1 := Version{Name: "v1", Entries: []Entry{
		entry("a", "v1", 0, []byte("alpha")),
		entry("b", "v1", 1, []byte("bravo")),
		entry("c", "v1", 2, []byte("charlie")),
	}}
	moved := entry("a", "v2", 7, []byte("alpha"))
	badByte := entry("b", "v2", 1, []byte("BRAVO")) // same unit, other bytes
	v2 := Version{Name: "v2", Entries: []Entry{moved, badByte}}
	var rep fault.Report
	m, err := Build([]Version{v1, v2}, &rep)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Has(fault.CodeAssetMissing, fault.CodeIndexConflict, fault.CodeByteConflict) {
		t.Fatalf("expected all three conflict classes, got %v", rep.All())
	}
	if rep.CountCode(fault.CodeAssetMissing) != 1 ||
		rep.CountCode(fault.CodeIndexConflict) != 1 ||
		rep.CountCode(fault.CodeByteConflict) != 1 {
		t.Fatalf("unexpected counts: %v", rep.All())
	}
	if len(m.Assets) != 3 { // a winner still produced for the missing key c
		t.Fatalf("assets = %d, want 3", len(m.Assets))
	}
}

func TestDigestIsOrderIndependent(t *testing.T) {
	a := Version{Name: "a", Entries: []Entry{entry("k", "a", 1, []byte("x"))}}
	b := Version{Name: "b", Entries: []Entry{entry("k", "b", 1, []byte("x"))}}
	m1, _ := Build([]Version{a, b}, &fault.Report{})
	m2, _ := Build([]Version{b, a}, &fault.Report{})
	if m1.Digest != m2.Digest {
		t.Fatalf("digest depends on version order: %s != %s", m1.Digest, m2.Digest)
	}
}

func TestStagerCommitAndRollback(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStager(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := Version{Name: "v1", Entries: []Entry{entry("a", "v1", 0, []byte("alpha"))}}
	m, _ := Build([]Version{v}, &fault.Report{})

	if err := st.Stage(m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.FinalPath()); !os.IsNotExist(err) {
		t.Fatal("final manifest must not exist before commit")
	}
	if err := st.Commit(m); err != nil || !m.Committed {
		t.Fatalf("commit: %v committed=%v", err, m.Committed)
	}
	committed, err := os.ReadFile(st.FinalPath())
	if err != nil {
		t.Fatal(err)
	}

	// A second, invalid attempt stages then rolls back; the first manifest
	// survives byte-for-byte and the reason is persisted.
	m2, _ := Build([]Version{v}, &fault.Report{})
	if err := st.Stage(m2); err != nil {
		t.Fatal(err)
	}
	if err := st.Rollback(m2, "validator rejected: dangling index"); err != nil {
		t.Fatal(err)
	}
	if !m2.RolledBack || m2.Committed || m2.FailReason == "" {
		t.Fatalf("rollback state wrong: %+v", m2)
	}
	if got, _ := os.ReadFile(st.FinalPath()); string(got) != string(committed) {
		t.Fatal("rollback overwrote the committed manifest")
	}
	reason, err := os.ReadFile(st.ReasonPath())
	if err != nil || string(reason) == "" {
		t.Fatalf("rollback reason not kept: %v %q", err, reason)
	}
	if _, err := os.Stat(st.StagedPath()); !os.IsNotExist(err) {
		t.Fatal("staging file must be removed after rollback")
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	if _, err := Build(nil, &fault.Report{}); err == nil {
		t.Fatal("no versions must fail")
	}
	if _, err := Build([]Version{{Name: "v"}}, nil); err == nil {
		t.Fatal("nil report must fail")
	}
	if _, err := Build([]Version{{Name: ""}}, &fault.Report{}); err == nil {
		t.Fatal("empty version name must fail")
	}
	bad := []Version{{Name: "v", Entries: []Entry{{}}}}
	if _, err := Build(bad, &fault.Report{}); err == nil {
		t.Fatal("empty asset key must fail")
	}
}

func TestNewStagerCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "stage")
	st, err := NewStager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.FinalPath() == "" {
		t.Fatal("final path empty")
	}
}
