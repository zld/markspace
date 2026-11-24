package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/mediapipe/fixtures"
	"github.com/zld/mediapipe/media"
	"github.com/zld/mediapipe/runner"
)

func gen(t *testing.T, spec fixtures.Spec) (string, string) {
	t.Helper()
	dir := t.TempDir()
	res, err := fixtures.Generate(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	return res.MainPath, dir
}

func opts(input, work string) runner.Options {
	return runner.Options{
		Inputs: []string{input}, WorkDir: work,
		Segment: 2 * media.Time(media.NanoDen), Bitrates: []string{"500k", "1000k"},
	}
}

func TestRunGoodInput(t *testing.T) {
	input, dir := gen(t, fixtures.DefaultSpec("baseline.mp4"))
	work := filepath.Join(dir, "work")
	rep, code := runner.Run(opts(input, work))
	if code != runner.ExitOK {
		t.Fatalf("exit=%d report=%+v", code, rep)
	}
	if rep.SegmentCount != 4 {
		t.Fatalf("segments=%d", rep.SegmentCount)
	}
	if rep.ManifestSHA256 == "" {
		t.Fatal("manifest digest missing")
	}
	if rep.AVSyncErrorMS > 40 {
		t.Fatalf("sync error %dms", rep.AVSyncErrorMS)
	}
	if rep.PacketRecoveryRate != 1.0 {
		t.Fatalf("recovery rate=%v", rep.PacketRecoveryRate)
	}
	if rep.P95LatencyMS < 0 {
		t.Fatalf("negative p95")
	}
}

func TestRunIsRepeatable(t *testing.T) {
	input, dir := gen(t, fixtures.DefaultSpec("baseline.mp4"))
	work := filepath.Join(dir, "work")
	first, _ := runner.Run(opts(input, work))
	// Clear the work directory so nothing is reused.
	_ = os.RemoveAll(work)
	second, _ := runner.Run(opts(input, work))
	if first.ManifestSHA256 != second.ManifestSHA256 {
		t.Fatalf("manifest differs: %s vs %s", first.ManifestSHA256, second.ManifestSHA256)
	}
	if first.SegmentCount != second.SegmentCount {
		t.Fatal("segment count differs")
	}
}

func TestRunCorruptInputFails(t *testing.T) {
	input, dir := gen(t, func() fixtures.Spec {
		s := fixtures.DefaultSpec("corrupt.mp4")
		s.Corrupt = "corrupt_box"
		s.CorruptBox = "stco"
		return s
	}())
	work := filepath.Join(dir, "work")
	rep, code := runner.Run(opts(input, work))
	if code != runner.ExitFailures {
		t.Fatalf("exit=%d", code)
	}
	if rep.FailureCount != 1 {
		t.Fatalf("failures=%d", rep.FailureCount)
	}
	f := rep.Files[0]
	if f.Error == nil || f.Error.Kind == "" {
		t.Fatal("failure has no classified error")
	}
}

func TestRunBatchContinuesAfterFailure(t *testing.T) {
	dir := t.TempDir()
	if _, err := fixtures.Generate(fixtures.DefaultSpec("good.mp4"), dir); err != nil {
		t.Fatal(err)
	}
	bad := fixtures.DefaultSpec("bad.mp4")
	bad.Corrupt = "truncate_moov"
	if _, err := fixtures.Generate(bad, dir); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	rep, code := runner.Run(runner.Options{
		Inputs:  []string{filepath.Join(dir, "bad.mp4"), filepath.Join(dir, "good.mp4")},
		WorkDir: work, Segment: 2 * media.Time(media.NanoDen),
	})
	if code != runner.ExitFailures {
		t.Fatalf("exit=%d", code)
	}
	if rep.FailureCount != 1 || len(rep.Files) != 2 {
		t.Fatalf("failures=%d files=%d", rep.FailureCount, len(rep.Files))
	}
	// The good file still produced segments.
	var goodOK bool
	for _, f := range rep.Files {
		if filepath.Base(f.Path) == "good.mp4" && f.OK {
			goodOK = true
		}
	}
	if !goodOK {
		t.Fatal("good input not processed after failure")
	}
}

func TestRunInterruptAndResumeMatchUninterrupted(t *testing.T) {
	input, dir := gen(t, fixtures.DefaultSpec("baseline.mp4"))
	work := filepath.Join(dir, "work")

	full, code := runner.Run(opts(input, work))
	if code != runner.ExitOK {
		t.Fatalf("full run exit=%d", code)
	}

	_ = os.RemoveAll(work)
	o := opts(input, work)
	o.InterruptAfter = 1
	_, code = runner.Run(o)
	if code != runner.ExitInterrupted {
		t.Fatalf("interrupt exit=%d", code)
	}

	o.Resume = true
	o.InterruptAfter = 0
	resumed, code := runner.Run(o)
	if code != runner.ExitOK {
		t.Fatalf("resume exit=%d", code)
	}
	if resumed.ManifestSHA256 != full.ManifestSHA256 {
		t.Fatalf("resumed manifest differs from uninterrupted")
	}
	if resumed.SegmentCount != full.SegmentCount {
		t.Fatalf("resumed segments=%d want %d", resumed.SegmentCount, full.SegmentCount)
	}
}

func TestResumeDoesNotDuplicateSegments(t *testing.T) {
	input, dir := gen(t, fixtures.DefaultSpec("baseline.mp4"))
	work := filepath.Join(dir, "work")
	o := opts(input, work)
	o.InterruptAfter = 1
	if _, code := runner.Run(o); code != runner.ExitInterrupted {
		t.Fatalf("interrupt exit=%d", code)
	}
	o.Resume = true
	o.InterruptAfter = 0
	if _, code := runner.Run(o); code != runner.ExitOK {
		t.Fatalf("resume exit=%d", code)
	}
	// Running resume again must be a no-op with the same result.
	rep, code := runner.Run(o)
	if code != runner.ExitOK {
		t.Fatalf("second resume exit=%d", code)
	}
	if rep.SegmentCount != 4 {
		t.Fatalf("segments=%d after repeated resume", rep.SegmentCount)
	}
}

func manifestOut(r *runner.Report) string {
	for _, f := range r.Files {
		if f.OK {
			return f.ManifestSHA256
		}
	}
	return ""
}

func TestInputChangeInvalidatesCheckpoint(t *testing.T) {
	input, dir := gen(t, fixtures.DefaultSpec("baseline.mp4"))
	work := filepath.Join(dir, "work")
	first, code := runner.Run(opts(input, work))
	if code != runner.ExitOK {
		t.Fatalf("exit=%d", code)
	}

	// Mutate input bytes; the index digest and checkpoint must not be reused.
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(data) - 100; i < len(data); i++ {
		data[i] ^= 0xFF
	}
	if err := os.WriteFile(input, data, 0o644); err != nil {
		t.Fatal(err)
	}
	o := opts(input, work)
	o.Resume = true
	second, code := runner.Run(o)
	if code != runner.ExitOK {
		t.Fatalf("mutated run exit=%d report=%+v", code, second)
	}
	if second.Files[0].IndexDigest == first.Files[0].IndexDigest {
		t.Fatal("input change did not change the index digest")
	}
	if manifestOut(second) == manifestOut(first) {
		t.Fatal("input change did not change the manifest digest")
	}
	if second.ManifestSHA256 != manifestOut(second) {
		t.Fatal("single-input batch digest should equal the file's manifest digest")
	}
}

func TestRunRejectsBadArguments(t *testing.T) {
	rep, code := runner.Run(runner.Options{WorkDir: t.TempDir()})
	if code != runner.ExitArgument {
		t.Fatalf("exit=%d", code)
	}
	if rep.FailureCount != 1 {
		t.Fatalf("failures=%d", rep.FailureCount)
	}
}
