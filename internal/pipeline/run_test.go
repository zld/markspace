package pipeline

import (
	"path/filepath"
	"strings"
	"testing"
)

func testOpts(dir string) Options {
	return Options{
		Seed:           17,
		Workers:        4,
		Queue:          8,
		CheckpointPath: filepath.Join(dir, "continuity.state"),
		ManifestPath:   filepath.Join(dir, "continuity.json"),
	}
}

func collect() (func(string), *[]string) {
	lines := &[]string{}
	return func(s string) { *lines = append(*lines, s) }, lines
}

func TestDemoDeterministic(t *testing.T) {
	dir := t.TempDir()
	emit1, _ := collect()
	r1 := Run(testOpts(dir), emit1)
	if r1.ExitCode != ExitOK {
		t.Fatalf("exit = %d", r1.ExitCode)
	}
	emit2, _ := collect()
	r2 := Run(testOpts(dir), emit2)
	if r2.ExitCode != ExitOK {
		t.Fatalf("exit = %d", r2.ExitCode)
	}
	if r1.ManifestSHA256 != r2.ManifestSHA256 {
		t.Fatalf("hash %s != %s", r1.ManifestSHA256, r2.ManifestSHA256)
	}
	if r1.Events != 142 || r1.Committed != 142 {
		t.Fatalf("events=%d committed=%d", r1.Events, r1.Committed)
	}
}

func TestCancelAndResumeMatchesUninterrupted(t *testing.T) {
	dir := t.TempDir()
	opts := testOpts(dir)

	// Uninterrupted reference run.
	ref := Run(opts, func(string) {})
	if ref.ExitCode != ExitOK {
		t.Fatalf("reference exit = %d", ref.ExitCode)
	}

	// Cancel after 5 commits, then resume to completion.
	opts.Case = "cancel-after"
	opts.CancelAfter = 5
	emit, lines := collect()
	cr := Run(opts, emit)
	if cr.ExitCode != ExitInternal {
		t.Fatalf("cancel exit = %d", cr.ExitCode)
	}
	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "cancelled phase=commit committed=5") {
		t.Fatalf("missing cancel report:\n%s", joined)
	}

	opts.Case = ""
	opts.Resume = true
	rr := Run(opts, func(string) {})
	if rr.ExitCode != ExitOK {
		t.Fatalf("resume exit = %d", rr.ExitCode)
	}
	if rr.ManifestSHA256 != ref.ManifestSHA256 {
		t.Fatalf("resumed hash %s != uninterrupted %s", rr.ManifestSHA256, ref.ManifestSHA256)
	}
	if rr.Committed != ref.Committed {
		t.Fatalf("resumed committed %d != %d", rr.Committed, ref.Committed)
	}
}

func TestResumeAfterDone(t *testing.T) {
	dir := t.TempDir()
	opts := testOpts(dir)
	first := Run(opts, func(string) {})
	opts.Resume = true
	second := Run(opts, func(string) {})
	if second.ExitCode != ExitOK || second.ManifestSHA256 != first.ManifestSHA256 {
		t.Fatalf("exit=%d hash %s != %s", second.ExitCode, second.ManifestSHA256, first.ManifestSHA256)
	}
}

func TestTruncatedBlock(t *testing.T) {
	dir := t.TempDir()
	opts := testOpts(dir)
	opts.Case = "truncated-block"
	emit, lines := collect()
	r := Run(opts, emit)
	if r.ExitCode != ExitInput {
		t.Fatalf("exit = %d, want %d", r.ExitCode, ExitInput)
	}
	joined := strings.Join(*lines, "\n")
	for _, want := range []string{"EBML_TRUNCATED_DATA", "offset=", "/Segment/Cluster/SimpleBlock", "recovered_events=120"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("output missing %q:\n%s", want, joined)
		}
	}
}

func TestUnknownLengthBoundary(t *testing.T) {
	dir := t.TempDir()
	opts := testOpts(dir)
	opts.Case = "unknown-length-boundary"
	r := Run(opts, func(string) {})
	if r.ExitCode != ExitOK || r.Events != 142 {
		t.Fatalf("exit=%d events=%d", r.ExitCode, r.Events)
	}
}

func TestInvalidParams(t *testing.T) {
	dir := t.TempDir()
	opts := testOpts(dir)
	opts.Workers = 0
	if r := Run(opts, func(string) {}); r.ExitCode != ExitInput {
		t.Fatalf("workers=0 exit = %d", r.ExitCode)
	}
	opts = testOpts(dir)
	opts.Queue = 99999
	if r := Run(opts, func(string) {}); r.ExitCode != ExitInput {
		t.Fatalf("queue overflow exit = %d", r.ExitCode)
	}
	opts = testOpts(dir)
	opts.Case = "bogus"
	if r := Run(opts, func(string) {}); r.ExitCode != ExitInput {
		t.Fatalf("bad case exit = %d", r.ExitCode)
	}
}

func TestResumeWithoutCheckpoint(t *testing.T) {
	dir := t.TempDir()
	opts := testOpts(dir)
	opts.Resume = true
	if r := Run(opts, func(string) {}); r.ExitCode != ExitInput {
		t.Fatalf("exit = %d", r.ExitCode)
	}
}
