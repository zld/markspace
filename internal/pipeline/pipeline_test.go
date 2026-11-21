package pipeline

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"hlsflow/internal/config"
	"hlsflow/internal/fixture"
	"hlsflow/internal/integrity"
	"hlsflow/internal/m3u8"
	"hlsflow/internal/safeio"
	"hlsflow/internal/ts"
)

func TestBuildAndVerifyClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := fixture.Generate(dir, fixture.ScenarioClean, []int{800000, 1800000}); err != nil {
		t.Fatal(err)
	}
	job, inputRoot, outRoot := roots(t, dir)
	report, err := Build(job, inputRoot, outRoot, Options{
		TargetDuration: 2 * time.Second, Finalize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Renditions) != 2 {
		t.Fatalf("renditions = %d", len(report.Renditions))
	}
	for _, line := range report.Renditions {
		if line.Segments == 0 {
			t.Fatalf("rendition %s produced no segments", line.Rendition)
		}
		if line.Missing != 0 || line.ChecksumFailures != 0 {
			t.Fatalf("clean run had faults: %+v", line)
		}
	}
	vrep, err := Verify(job, outRoot)
	if err != nil {
		t.Fatal(err)
	}
	if vrep.HasFailures() {
		t.Fatalf("verify reported failures on clean tree: %+v", vrep.Renditions)
	}
}

func TestBuildAndVerifyFaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := fixture.Generate(dir, fixture.ScenarioFaults, []int{800000, 1800000, 3500000}); err != nil {
		t.Fatal(err)
	}
	job, inputRoot, outRoot := roots(t, dir)
	report, err := Build(job, inputRoot, outRoot, Options{
		TargetDuration: 2 * time.Second, Finalize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every rendition is missing source segment 5.
	var totalMissing int
	for _, line := range report.Renditions {
		totalMissing += line.Missing
		if line.Reordered < 2 {
			t.Fatalf("expected reorder accounting on %s, got %d", line.Rendition, line.Reordered)
		}
	}
	if totalMissing != 3 {
		t.Fatalf("missing = %d, want 3", totalMissing)
	}
	// The first rendition carries the truncated-packet fault.
	foundTrunc := false
	for _, line := range report.Renditions {
		for _, f := range line.Failures {
			if f.Reason == "truncated packet at segment tail" {
				foundTrunc = true
			}
		}
	}
	if !foundTrunc {
		t.Fatal("truncation fault not surfaced")
	}
	vrep, err := Verify(job, outRoot)
	if err != nil {
		t.Fatal(err)
	}
	var checksumFails int
	var sawDrift bool
	for _, line := range vrep.Renditions {
		checksumFails += line.ChecksumFailures
		if line.DriftSeconds != 0 {
			sawDrift = true
		}
	}
	if checksumFails != 1 {
		t.Fatalf("checksum failures = %d, want 1 (injected fault)", checksumFails)
	}
	if !sawDrift {
		t.Fatal("verify did not recompute cross-rendition drift from the index")
	}
}

func TestVerifyDetectsExtraFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := fixture.Generate(dir, fixture.ScenarioClean, []int{800000}); err != nil {
		t.Fatal(err)
	}
	job, inputRoot, outRoot := roots(t, dir)
	if _, err := Build(job, inputRoot, outRoot, Options{TargetDuration: 2 * time.Second, Finalize: true}); err != nil {
		t.Fatal(err)
	}
	if err := outRoot.AtomicWrite("480p/orphan.ts", []byte{0x47}); err != nil {
		t.Fatal(err)
	}
	vrep, err := Verify(job, outRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(vrep.ExtraFiles) != 1 {
		t.Fatalf("extras = %v", vrep.ExtraFiles)
	}
}

func TestIncrementalPublishesAreParseable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if _, err := fixture.Generate(dir, fixture.ScenarioClean, []int{800000}); err != nil {
		t.Fatal(err)
	}
	job, inputRoot, outRoot := roots(t, dir)
	rd := job.Renditions[0]
	index := integrity.New()
	var publishes int
	cb := func(pl m3u8.MediaPlaylist, idx *integrity.Index) error {
		publishes++
		parsed, err := m3u8.ParseMedia(bytes.NewReader(pl.Bytes()))
		if err != nil {
			return err
		}
		if parsed.Closed {
			return fmt.Errorf("intermediate playlist must stay open (EVENT)")
		}
		if len(parsed.Segments) == 0 {
			return fmt.Errorf("intermediate playlist had no segments")
		}
		return idx.Save(outRoot)
	}
	br, _, err := processRendition(rd, inputRoot, outRoot, 2*ts.TicksPerSecond,
		config.Duration{}, index, cb)
	if err != nil {
		t.Fatal(err)
	}
	if publishes != rd.ExpectedCount() {
		t.Fatalf("publishes = %d, want %d", publishes, rd.ExpectedCount())
	}
	if br.report.Segments == 0 {
		t.Fatal("no segments built")
	}
}

func roots(t *testing.T, dir string) (*config.Job, *safeio.Root, *safeio.Root) {
	t.Helper()
	job, err := config.Load(filepath.Join(dir, "job.json"))
	if err != nil {
		t.Fatal(err)
	}
	inputRoot, err := safeio.NewRoot(filepath.Join(dir, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	outRoot, err := safeio.EnsureRoot(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	return job, inputRoot, outRoot
}
