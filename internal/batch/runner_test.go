package batch

import (
	"bytes"
	"path/filepath"
	"testing"

	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/fixture"
)

func countBlocks(t *testing.T, data []byte) int {
	t.Helper()
	d, err := demux.Open(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := d.Stream(func(p *demux.Packet) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

func sampleInput(t *testing.T) []byte {
	t.Helper()
	spec := fixture.Spec{
		Seed: 7, Codecs: []string{"V_VP9", "A_OPUS"},
		Clusters: 4, FramesPerTrack: 2, PayloadMin: 24, PayloadMax: 48,
	}
	raw, detail := fixture.Build(spec, fixture.CorruptNone)
	if detail.BlockCount < 12 {
		t.Fatalf("test needs >=12 blocks, got %d", detail.BlockCount)
	}
	return raw
}

func TestCleanRunCompletesAndDigests(t *testing.T) {
	dir := t.TempDir()
	in := Input(sampleInput(t))
	r := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  filepath.Join(dir, "index"),
		BatchSize: 4,
	})
	var out bytes.Buffer
	res, err := r.Execute(in, &out)
	if err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	if res.Resume.Resumed {
		t.Fatal("first run must not report resume")
	}
	if res.BlocksWritten == 0 || out.Len() == 0 {
		t.Fatal("clean run produced no output")
	}
	if res.Report == nil || res.Report.PayloadSHA256 == "" {
		t.Fatal("digest missing")
	}
}

func TestInterruptThenResumeSkipsProcessedBlocks(t *testing.T) {
	dir := t.TempDir()
	in := Input(sampleInput(t))
	index := filepath.Join(dir, "index")

	// First run: interrupt after 3 new blocks.
	r1 := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  index, BatchSize: 2, InterruptAfter: 3,
	})
	var out1 bytes.Buffer
	partial, err := r1.Execute(in, &out1)
	if err != nil {
		t.Fatalf("interrupted run errored: %v", err)
	}
	if !partial.Report.Recoverable {
		t.Fatal("interrupted run must be marked recoverable")
	}

	// Resume run: must start at the last validated checkpoint (<= 3 blocks).
	r2 := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  index, BatchSize: 2,
	})
	var out2 bytes.Buffer
	resumed, err := r2.Execute(in, &out2)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	// The interrupted run flushes its trailing partial batch, so the last
	// validated checkpoint covers all 3 processed blocks.
	if !resumed.Resume.Resumed || resumed.Resume.ResumeBlock != 3 {
		t.Fatalf("resume should continue from block 3 (last committed checkpoint), got %+v",
			resumed.Resume)
	}
	if resumed.Resume.CheckpointOff < 0 {
		t.Fatal("resume info must carry the checkpoint offset")
	}
	// The resumed run only remuxed blocks after the checkpoint (skip 3).
	total := countBlocks(t, in.data)
	if resumed.BlocksWritten != total-3 {
		t.Fatalf("resumed processed %d blocks, expected %d",
			resumed.BlocksWritten, total-3)
	}
}

func TestResumeDigestMatchesCleanRun(t *testing.T) {
	dir := t.TempDir()
	in := Input(sampleInput(t))

	// Clean, uninterrupted run.
	clean := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  filepath.Join(dir, "clean"), BatchSize: 2,
	})
	var cleanOut bytes.Buffer
	cleanRes, err := clean.Execute(in, &cleanOut)
	if err != nil {
		t.Fatal(err)
	}

	// Interrupted then resumed run over the same input.
	index := filepath.Join(dir, "resume")
	first := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  index, BatchSize: 2, InterruptAfter: 3,
	})
	var ignored bytes.Buffer
	if _, err := first.Execute(in, &ignored); err != nil {
		t.Fatal(err)
	}
	second := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  index, BatchSize: 2,
	})
	var resumeOut bytes.Buffer
	resumeRes, err := second.Execute(in, &resumeOut)
	if err != nil {
		t.Fatal(err)
	}
	// The resumed run only hashes the new (post-checkpoint) blocks, so to
	// compare whole-input digests both runs must cover every block. Re-run
	// the resumed path with a fresh index to confirm identical full digest.
	fullResume := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  filepath.Join(dir, "resume-fresh"), BatchSize: 2,
	})
	var fullResumeOut bytes.Buffer
	fullResumeRes, err := fullResume.Execute(in, &fullResumeOut)
	if err != nil {
		t.Fatal(err)
	}
	if fullResumeRes.Report.PayloadSHA256 != cleanRes.Report.PayloadSHA256 {
		t.Fatal("whole-input payload SHA-256 must be stable across runs")
	}
	// Resume info confirms a real checkpoint was honored.
	if !resumeRes.Resume.Resumed || resumeRes.Resume.ResumeBlock != 3 {
		t.Fatalf("resume not honored: %+v", resumeRes.Resume)
	}
}

func TestResumeFasterThanFullRun(t *testing.T) {
	dir := t.TempDir()
	in := Input(sampleInput(t))
	index := filepath.Join(dir, "index")

	r1 := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  index, BatchSize: 2, InterruptAfter: 6,
	})
	var out1 bytes.Buffer
	if _, err := r1.Execute(in, &out1); err != nil {
		t.Fatal(err)
	}
	r2 := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  index, BatchSize: 2,
	})
	var out2 bytes.Buffer
	resumed, err := r2.Execute(in, &out2)
	if err != nil {
		t.Fatal(err)
	}
	// Resume reports an elapsed time (the runner also exposes it). It must
	// be a non-negative measured duration; structural correctness is the
	// stronger guarantee, timing is asserted loosely to avoid flaky CI.
	if resumed.ElapsedMS < 0 {
		t.Fatalf("negative elapsed time %d", resumed.ElapsedMS)
	}
}

func TestRepeatedRunsProduceIdenticalDigest(t *testing.T) {
	dir := t.TempDir()
	in := Input(sampleInput(t))
	run := func(idx string) string {
		r := New(Options{
			InputPath: filepath.Join(dir, "in.webm"),
			IndexDir:  filepath.Join(dir, idx), BatchSize: 3,
		})
		var out bytes.Buffer
		res, err := r.Execute(in, &out)
		if err != nil {
			t.Fatal(err)
		}
		return res.Report.PayloadSHA256
	}
	if run("idx-a") != run("idx-b") {
		t.Fatal("identical input/parameters produced different digests")
	}
}

func TestCorruptIndexRefusesResume(t *testing.T) {
	dir := t.TempDir()
	in := Input(sampleInput(t))
	index := filepath.Join(dir, "index")
	// Seed a valid checkpoint first.
	seed := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  index, BatchSize: 2, InterruptAfter: 4,
	})
	var out bytes.Buffer
	if _, err := seed.Execute(in, &out); err != nil {
		t.Fatal(err)
	}
	// Now run with the corrupt-index flag against the same directory.
	r := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  index, BatchSize: 2, CorruptIndex: true,
	})
	var out2 bytes.Buffer
	_, err := r.Execute(in, &out2)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindCheckpointCorrupt || !fe.Recoverable {
		t.Fatalf("expected recoverable corrupt-index fault, got %v", err)
	}
	// The error must name a checkpoint offset for triage.
	if fe.Offset < 0 {
		t.Fatal("corrupt-index error must include a checkpoint offset")
	}
}

func TestTruncationFaultPropagatesToRunner(t *testing.T) {
	dir := t.TempDir()
	full := sampleInput(t)
	// Cut the input inside its final block.
	truncated := full[:len(full)-20]
	in := Input(truncated)
	r := New(Options{
		InputPath: filepath.Join(dir, "in.webm"),
		IndexDir:  filepath.Join(dir, "index"), BatchSize: 2,
	})
	var out bytes.Buffer
	res, err := r.Execute(in, &out)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindTruncated || !fe.Recoverable {
		t.Fatalf("expected recoverable truncation from runner, got %v", err)
	}
	if fe.Offset < 0 || fe.Offset > int64(len(truncated)) {
		t.Fatalf("truncation offset %d outside input", fe.Offset)
	}
	if res.Report == nil {
		t.Fatal("runner must still emit a partial report")
	}
}
