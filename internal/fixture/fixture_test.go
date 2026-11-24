package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/manifest"
	"mediasync/internal/mp4"
)

func TestGenerateIsByteStable(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	cfg := DefaultCaptureConfig(17)
	cfg.DurationMS = 2000
	cfg.Cameras = 2

	m1, e1, err := Generate(dir1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	m2, e2, err := Generate(dir2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	d1, _ := os.ReadFile(m1)
	d2, _ := os.ReadFile(m2)
	if string(d1) != string(d2) {
		t.Error("manifest is not byte-stable for the same seed")
	}
	c1, _ := os.ReadFile(e1)
	c2, _ := os.ReadFile(e2)
	if string(c1) != string(c2) {
		t.Error("capture log is not byte-stable for the same seed")
	}
}

func TestGenerateDifferentSeedsDiffer(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	cfg := DefaultCaptureConfig(1)
	cfg.DurationMS = 3000
	_, ea, _ := Generate(a, cfg)
	cfg.Seed = 2
	_, eb, _ := Generate(b, cfg)
	ca, _ := os.ReadFile(ea)
	cb, _ := os.ReadFile(eb)
	if string(ca) == string(cb) {
		t.Error("different seeds produced identical logs")
	}
}

func TestGeneratedManifestParses(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultCaptureConfig(5)
	cfg.DurationMS = 4000
	mp, _, err := Generate(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.ParseFile(mp)
	if err != nil {
		t.Fatalf("generated manifest does not parse: %v", err)
	}
	if len(m.Tracks) != cfg.Cameras+1 {
		t.Errorf("tracks = %d, want %d", len(m.Tracks), cfg.Cameras+1)
	}
	for _, tr := range m.Tracks {
		if tr.StartTicks <= 0 {
			t.Errorf("track %d has a zero start; the fixture must exercise non-zero starts", tr.ID)
		}
		if len(tr.Edit) == 0 {
			t.Errorf("track %d has no edit list", tr.ID)
		}
		if len(tr.Keyframes) < 2 {
			t.Errorf("track %d has no sparse keyframe index", tr.ID)
		}
	}
}

func TestMaterializeCorruptFixture(t *testing.T) {
	dir := t.TempDir()
	path, at, err := Materialize("corrupt-truncated", dir)
	if err != nil {
		t.Fatal(err)
	}
	if at != DamageOffset {
		t.Errorf("injected offset = %d, want %d", at, DamageOffset)
	}
	info, ierr := mp4.InspectFile(path)
	if ierr == nil {
		t.Fatal("corrupt fixture inspected cleanly")
	}
	if info == nil || len(info.Diagnostics) == 0 {
		t.Fatal("no diagnostics for the corrupt fixture")
	}
	if got := info.Diagnostics[0].Loc.ByteOffset; got != DamageOffset {
		t.Errorf("located offset = %d, want %d", got, DamageOffset)
	}
}

func TestMaterializeEditOverflowFixture(t *testing.T) {
	dir := t.TempDir()
	path, _, err := Materialize("edit-out-of-range", dir)
	if err != nil {
		t.Fatal(err)
	}
	info, ierr := mp4.InspectFile(path)
	if ierr == nil {
		t.Fatal("edit overflow fixture inspected cleanly")
	}
	if info == nil || len(info.Diagnostics) == 0 {
		t.Fatal("no diagnostics")
	}
	if info.Diagnostics[0].Loc.TrackID == 0 {
		t.Error("edit diagnostic must name the track")
	}
}

func TestMaterializeCleanFixture(t *testing.T) {
	dir := t.TempDir()
	path, at, err := Materialize("generated-long", dir)
	if err != nil {
		t.Fatal(err)
	}
	if at != 0 {
		t.Errorf("clean fixture reported damage at %d", at)
	}
	if _, err := mp4.InspectFile(path); err != nil {
		t.Fatalf("clean fixture should inspect cleanly: %v", err)
	}
}

func TestMaterializeUnknown(t *testing.T) {
	if _, _, err := Materialize("no-such-fixture", t.TempDir()); err == nil {
		t.Error("unknown fixture must be rejected")
	}
	if _, _, err := Materialize("", t.TempDir()); err == nil {
		t.Error("empty fixture name must be rejected")
	}
}

func TestMaterializePathPassthrough(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "existing.mp4")
	if err := WriteFile(clean, Spec()); err != nil {
		t.Fatal(err)
	}
	got, at, err := Materialize(clean, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != clean || at != 0 {
		t.Errorf("passthrough = %q,%d", got, at)
	}
}
