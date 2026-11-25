package scan

import (
	"path/filepath"
	"testing"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
)

func generate(t *testing.T, c fixture.Case, reps, frags int) []fixture.RepresentationDescriptor {
	t.Helper()
	cfg := fixture.Config{
		Root:            filepath.Join(t.TempDir(), "gen"),
		Case:            c,
		Representations: reps,
		Fragments:       frags,
	}
	descs, err := fixture.Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return descs
}

func TestReadInitSegment(t *testing.T) {
	descs := generate(t, fixture.CaseNormal, 2, 5)
	info, err := ReadInitSegment(filepath.Join(descs[0].Directory, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Timescale != 90000 || info.TrackID != 1 || info.SHA256 == "" {
		t.Fatalf("unexpected init info: %+v", info)
	}
}

func TestReadFragmentExtractsMfhdAndTfdt(t *testing.T) {
	descs := generate(t, fixture.CaseNormal, 1, 3)
	for i := 1; i <= 3; i++ {
		path := filepath.Join(descs[0].Directory, fixture.FragmentName(uint32(i)))
		f, err := ReadFragment(i-1, descs[0].ID, path)
		if err != nil {
			t.Fatalf("fragment %d: %v", i, err)
		}
		if f.MFHD != uint32(i) {
			t.Errorf("mfhd = %d, want %d", f.MFHD, i)
		}
		wantTFDT := descs[0].TickDuration * uint64(i-1)
		if f.TFDT != wantTFDT {
			t.Errorf("tfdt = %d, want %d", f.TFDT, wantTFDT)
		}
		if f.SHA256 == "" || f.Size == 0 {
			t.Error("digest and size must be populated")
		}
	}
}

func TestTruncatedBoxRejected(t *testing.T) {
	descs := generate(t, fixture.CaseTruncatedBox, 1, 5)
	bad := filepath.Join(descs[0].Directory, fixture.FragmentName(fixture.RegressionSeqForTest()))
	_, err := ReadFragment(2, descs[0].ID, bad)
	if !fault.Is(err, fault.CodeTruncatedBox) {
		t.Fatalf("expected truncated_box, got %v", err)
	}
}

func TestMissingRequiredBoxRejected(t *testing.T) {
	dir := t.TempDir()
	// styp + moof only, no mdat.
	data := append(brandForTest(), moofOnlyForTest()...)
	path := filepath.Join(dir, "frag-001.m4s")
	if err := writeTestFile(path, data); err != nil {
		t.Fatal(err)
	}
	_, err := ReadFragment(0, "rep0", path)
	if !fault.Is(err, fault.CodeTruncatedBox) {
		t.Fatalf("expected truncated_box for missing mdat, got %v", err)
	}
}
