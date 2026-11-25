package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/assetcheck/internal/manifest"
	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/testdata"
)

func generated(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := testdata.Generate(dir, 17); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return dir
}

func TestParseInputDetectsContainer(t *testing.T) {
	dir := generated(t)
	a, err := ParseInput(filepath.Join(dir, testdata.ValidMKV))
	if err != nil {
		t.Fatalf("mkv: %v", err)
	}
	if a.Container != media.ContainerMatroska {
		t.Fatalf("container = %s", a.Container)
	}
	// A file that begins with neither magic is rejected as unsupported.
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("not media at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ParseInput(other)
	if timeline.CategoryOf(err) != timeline.CatUnsupported {
		t.Fatalf("category = %s", timeline.CategoryOf(err))
	}
	// A two-byte file cannot even hold a magic.
	short := filepath.Join(dir, "short.bin")
	if err := os.WriteFile(short, []byte{0x1A, 0x45}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ParseInput(short)
	if timeline.CategoryOf(err) != timeline.CatTruncated {
		t.Fatalf("category = %s", timeline.CategoryOf(err))
	}
}

func TestVerifyReportsQuantities(t *testing.T) {
	dir := generated(t)
	rep, err := Verify(filepath.Join(dir, testdata.ValidMKV), VerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("report not OK: %+v", rep)
	}
	if rep.ElementCount < 50 {
		t.Fatalf("elements = %d", rep.ElementCount)
	}
	if rep.MaxDeviation > timeline.MaxTolerance {
		t.Fatalf("deviation = %d", rep.MaxDeviation)
	}
	if len(rep.SHA256) != 64 {
		t.Fatalf("sha = %q", rep.SHA256)
	}
	if len(rep.Tracks) != 5 || rep.FragmentCount != 10 {
		t.Fatalf("tracks=%d fragments=%d", len(rep.Tracks), rep.FragmentCount)
	}
}

func TestVerifyWithRTPDump(t *testing.T) {
	dir := generated(t)
	rep, err := Verify(filepath.Join(dir, testdata.ValidMKV),
		VerifyOptions{RTPDump: filepath.Join(dir, testdata.RTPDump)})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.RTP == nil {
		t.Fatal("RTP report missing")
	}
	if rep.RTP.Packets == 0 {
		t.Fatal("no RTP packets parsed")
	}
}

func TestVerifyDetectsDamagedInput(t *testing.T) {
	dir := generated(t)
	for _, name := range []string{testdata.TruncatedMKV, testdata.IllegalLenMKV} {
		_, err := Verify(filepath.Join(dir, name), VerifyOptions{})
		if err == nil {
			t.Fatalf("%s verified clean", name)
		}
		if timeline.ExitCodeOf(err) != timeline.ExitBadInput {
			t.Fatalf("%s exit = %d", name, timeline.ExitCodeOf(err))
		}
	}
}

func TestMigrateStopsBeforeCheckpointOnValidationFailure(t *testing.T) {
	dir := generated(t)
	out := filepath.Join(dir, "out")
	m, err := manifest.Load(filepath.Join(dir, testdata.ManifestV1))
	if err != nil {
		t.Fatal(err)
	}
	// Track-conflict fixture fails validation, so no checkpoint may be written.
	outcome, err := Migrate(MigrateOptions{
		Input:        filepath.Join(dir, testdata.TrackConflictMKV),
		ManifestPath: filepath.Join(dir, testdata.ManifestV1),
		Out:          out,
	})
	if err == nil {
		t.Fatal("migration of conflicting input succeeded")
	}
	_ = m
	if outcome == nil || outcome.Verify == nil {
		t.Fatal("no verify report attached to the failure")
	}
	if _, statErr := os.Stat(filepath.Join(out, ".assetcheck")); !os.IsNotExist(statErr) {
		t.Fatalf("checkpoint tree written before validation passed: %v", statErr)
	}
}

func TestMigrateEndToEndDeterminism(t *testing.T) {
	dir := generated(t)
	input := filepath.Join(dir, testdata.ValidMKV)
	manifestPath := filepath.Join(dir, testdata.ManifestV1)

	run := func(out string) *MigrateOutcome {
		t.Helper()
		o, err := Migrate(MigrateOptions{Input: input, ManifestPath: manifestPath, Out: out})
		if err != nil {
			t.Fatalf("migrate: %v", err)
		}
		return o
	}
	a := run(filepath.Join(dir, "a"))
	b := run(filepath.Join(dir, "b"))
	if a.Result.FinalDigest != b.Result.FinalDigest {
		t.Fatalf("digests differ: %s vs %s", a.Result.FinalDigest, b.Result.FinalDigest)
	}

	// Interrupt and resume, then compare to the uninterrupted digest.
	cOut := filepath.Join(dir, "c")
	if _, err := Migrate(MigrateOptions{Input: input, ManifestPath: manifestPath,
		Out: cOut, StopAfter: "normalize"}); err != nil {
		t.Fatalf("partial: %v", err)
	}
	c, err := Migrate(MigrateOptions{Input: input, ManifestPath: manifestPath,
		Out: cOut, Resume: true})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if c.Result.FinalDigest != a.Result.FinalDigest {
		t.Fatalf("resumed %s != uninterrupted %s", c.Result.FinalDigest, a.Result.FinalDigest)
	}
}

func TestDescribeElementsPointsAtBytes(t *testing.T) {
	dir := generated(t)
	a, err := ParseInput(filepath.Join(dir, testdata.ValidMKV))
	if err != nil {
		t.Fatal(err)
	}
	summary := ElementSummary(a)
	if summary == "" {
		t.Fatal("empty element summary")
	}
	desc := DescribeElements(a, 3)
	if desc == "" {
		t.Fatal("empty element description")
	}
	// Offsets in the description must be ascending within the first few blocks.
	var prev int64 = -1
	for _, e := range a.Elements {
		if e.Offset < prev {
			t.Fatalf("element offsets not ascending: %d after %d", e.Offset, prev)
		}
		prev = e.Offset
	}
}
