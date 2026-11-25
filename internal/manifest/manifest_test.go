package manifest

import (
	"path/filepath"
	"testing"

	"mediapipeline/internal/align"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/scan"
)

func alignedReport() *align.Report {
	return &align.Report{
		RepresentationCount: 2,
		ToleranceNanos:      20833,
		Boundaries: []align.Boundary{
			{
				Index: 0, StartNanos: 0, EndNanos: 100, Aligned: true,
				Members: map[string]*align.Member{
					"rep0": {MFHD: 1, TFDT: 0, SHA256: "a0", InputPath: "rep0/frag-001.m4s"},
					"rep1": {MFHD: 1, TFDT: 0, SHA256: "b0", InputPath: "rep1/frag-001.m4s"},
				},
			},
		},
	}
}

func scanReports() []*scan.TrackReport {
	return []*scan.TrackReport{
		{
			Representation: "rep0",
			Gaps:           []scan.Gap{{MissingMFHD: 3, AfterMFHD: 2}},
			Regressions: []scan.Regression{{
				InputPath: "rep0/frag-003.m4s", MFHD: 3,
				PreviousTFDT: 18000, ObservedTFDT: 100, InputSHA256: "reg",
			}},
		},
	}
}

func TestBuildAndValidate(t *testing.T) {
	m := Build("v1", alignedReport(), scanReports(), 20833)
	if err := m.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if m.SegmentCount != 1 || m.GapCount != 1 || m.RegressionCount != 1 {
		t.Fatalf("counts wrong: %+v", m)
	}
	h1, _ := m.SHA256()
	h2, _ := Build("v1", alignedReport(), scanReports(), 20833).SHA256()
	if h1 != h2 {
		t.Fatal("identical inputs must yield identical manifest digest")
	}
}

func TestValidateRejectsMisaligned(t *testing.T) {
	bad := alignedReport()
	bad.Boundaries[0].Aligned = false
	m := Build("v1", bad, nil, 20833)
	err := m.Validate()
	if !fault.Is(err, fault.CodeMisaligned) {
		t.Fatalf("expected misaligned error, got %v", err)
	}
}

func TestValidateRejectsMissingMemberAndEmptySegments(t *testing.T) {
	short := alignedReport()
	delete(short.Boundaries[0].Members, "rep1")
	m := Build("v1", short, nil, 20833)
	if err := m.Validate(); !fault.Is(err, fault.CodeMisaligned) {
		t.Fatalf("missing member should be misaligned: %v", err)
	}
	empty := Build("v1", &align.Report{RepresentationCount: 2}, nil, 1)
	if err := empty.Validate(); !fault.Is(err, fault.CodeManifest) {
		t.Fatalf("empty manifest should fail validation: %v", err)
	}
}

func TestWriteAtomicProtectsPriorArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "manifest.json")
	first := Build("v1", alignedReport(), nil, 20833)
	digest1, replaced, err := first.WriteAtomic(path)
	if err != nil || !replaced {
		t.Fatalf("first write should replace: %v %v", replaced, err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	digestLoaded, _ := loaded.SHA256()
	if digestLoaded != digest1 {
		t.Fatal("persisted manifest digest mismatch")
	}
	// A second valid manifest (even with a different version) must not
	// overwrite the existing successful artifact.
	second := Build("v2", alignedReport(), nil, 20833)
	digest2, replaced, err := second.WriteAtomic(path)
	if err != nil || replaced {
		t.Fatalf("prior artifact must be protected: replaced=%v err=%v", replaced, err)
	}
	if digest2 != digest1 {
		t.Fatal("protected write should return the existing digest")
	}
}

func TestWriteAtomicRejectsInvalidBeforeTouchingDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	bad := alignedReport()
	bad.Boundaries[0].Aligned = false
	m := Build("v1", bad, nil, 20833)
	if _, _, err := m.WriteAtomic(path); err == nil {
		t.Fatal("invalid manifest must not be written")
	}
	if _, err := Load(path); err == nil {
		t.Fatal("no file should exist after a rejected write")
	}
}
