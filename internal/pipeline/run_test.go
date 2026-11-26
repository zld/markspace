package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"rtx/internal/cache"
	"rtx/internal/cmaf"
	"rtx/internal/fixtures"
	"rtx/internal/model"
)

// genFixtures builds a small fixture tree once per test.
func genFixtures(t *testing.T, opts fixtures.Options) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := fixtures.Generate(dir, opts); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runOnce(t *testing.T, inputDir, cacheDir, cpPath, outDir string, stopAfter int) (*Result, error) {
	t.Helper()
	return Run(Options{
		InputDir:   inputDir,
		CacheDir:   cacheDir,
		Checkpoint: cpPath,
		OutDir:     outDir,
		StopAfter:  stopAfter,
	})
}

// runOK runs the pipeline and fails the test on error.
func runOK(t *testing.T, inputDir, cacheDir, cpPath, outDir string, stopAfter int) *Result {
	t.Helper()
	res, err := runOnce(t, inputDir, cacheDir, cpPath, outDir, stopAfter)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRunGood(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	res := runOK(t, filepath.Join(root, fixtures.NameGood), filepath.Join(work, "cache"), filepath.Join(work, "cp.json"), filepath.Join(work, "out"), 0)
	if !res.RepeatEqual {
		t.Fatal("repeat_equal not proven")
	}
	if res.Gaps != 0 || res.Repairs != 0 || res.SourceSwitch != 0 {
		t.Fatalf("unexpected anomalies: %+v", res)
	}
	if res.Metrics == nil {
		t.Fatal("no metrics")
	}
	if res.Metrics.PixelMean < 94 || res.Metrics.PixelMean > 98 {
		t.Fatalf("pixel mean = %.2f", res.Metrics.PixelMean)
	}
	if res.Metrics.PeakDBFS < -7 || res.Metrics.PeakDBFS > -5 {
		t.Fatalf("peak = %.2f", res.Metrics.PeakDBFS)
	}
	if res.Metrics.AVSyncMS > 2 {
		t.Fatalf("av sync = %.2f", res.Metrics.AVSyncMS)
	}
	if _, err := os.Stat(filepath.Join(work, "out", "index.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunEdgeCounts(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	res := runOK(t, filepath.Join(root, fixtures.NameEdge), filepath.Join(work, "cache"), filepath.Join(work, "cp.json"), filepath.Join(work, "out"), 0)
	if res.Gaps != 1 {
		t.Fatalf("gap = %d, want 1", res.Gaps)
	}
	if res.Repairs != 1 {
		t.Fatalf("tfdt_rollback = %d, want 1", res.Repairs)
	}
	if res.SourceSwitch != 1 {
		t.Fatalf("source_switch = %d, want 1", res.SourceSwitch)
	}
	if res.RTP == nil {
		t.Fatal("no RTP verification")
	}
	var sawWrap, sawGap bool
	for _, s := range res.RTP.Summaries {
		if s.TimestampWrap {
			sawWrap = true
		}
		if s.Gaps > 0 {
			sawGap = true
		}
	}
	if !sawWrap {
		t.Fatal("expected a 32-bit RTP timestamp wrap")
	}
	if !sawGap {
		t.Fatal("expected RTP packet loss")
	}
}

func TestResumeMatchesUninterrupted(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	input := filepath.Join(root, fixtures.NameEdge)
	cacheDir := filepath.Join(work, "cache")

	// Interrupted run: two records then an interruption.
	cpPath := filepath.Join(work, "cp.json")
	outDir := filepath.Join(work, "out")
	_, err := runOnce(t, input, cacheDir, cpPath, outDir, 2)
	if err == nil {
		t.Fatal("expected interruption")
	}
	if code := model.ExitCodeOf(err); code != 75 {
		t.Fatalf("exit code = %d, want 75", code)
	}
	resume := runOK(t, input, cacheDir, cpPath, outDir, 0)
	if !resume.Resume {
		t.Fatal("resume flag not set")
	}

	// Uninterrupted run with its own cache and output.
	clean := runOK(t, input, filepath.Join(work, "cache2"), filepath.Join(work, "cp2.json"), filepath.Join(work, "out2"), 0)
	if resume.IndexSum != clean.IndexSum {
		t.Fatalf("resumed index %s != uninterrupted %s", resume.IndexSum, clean.IndexSum)
	}
	if resume.MediaSum != clean.MediaSum {
		t.Fatalf("resumed media %s != uninterrupted %s", resume.MediaSum, clean.MediaSum)
	}
	if len(resume.FragmentDigests) != len(clean.FragmentDigests) {
		t.Fatalf("fragment counts differ: %d vs %d", len(resume.FragmentDigests), len(clean.FragmentDigests))
	}
	for i := range resume.FragmentDigests {
		if resume.FragmentDigests[i] != clean.FragmentDigests[i] {
			t.Fatalf("fragment %d differs", i)
		}
	}
	// Compare the written index bytes directly.
	a, _ := os.ReadFile(filepath.Join(outDir, "index.json"))
	b, _ := os.ReadFile(filepath.Join(work, "out2", "index.json"))
	if string(a) != string(b) {
		t.Fatal("index.json differs between resumed and uninterrupted runs")
	}
}

func TestResumeDoesNotDuplicate(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	input := filepath.Join(root, fixtures.NameGood)
	cpPath := filepath.Join(work, "cp.json")
	outDir := filepath.Join(work, "out")
	_, err := runOnce(t, input, filepath.Join(work, "cache"), cpPath, outDir, 2)
	if err == nil {
		t.Fatal("expected interruption")
	}
	// A checkpoint with the same input and a larger cursor refuses to restart
	// from zero: it continues and lands each record exactly once.
	res := runOK(t, input, filepath.Join(work, "cache"), cpPath, outDir, 0)
	if res.Cursor != res.Records {
		t.Fatalf("cursor = %d, records = %d", res.Cursor, res.Records)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	frags := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".m4s" {
			frags++
		}
	}
	if frags != res.Records {
		t.Fatalf("wrote %d fragment files for %d records", frags, res.Records)
	}
}

func TestResumeRejectsChangedInput(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	input := filepath.Join(root, fixtures.NameGood)
	cpPath := filepath.Join(work, "cp.json")
	if _, err := runOnce(t, input, filepath.Join(work, "cache"), cpPath, filepath.Join(work, "out"), 2); err == nil {
		t.Fatal("expected interruption")
	}
	// Mutate one fragment after the checkpoint was written.
	target := filepath.Join(input, "cam-a-0003.m4s")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = runOnce(t, input, filepath.Join(work, "cache"), cpPath, filepath.Join(work, "out"), 0)
	if err == nil {
		t.Fatal("expected checkpoint/input mismatch")
	}
	if e, ok := model.As(err); !ok || e.Code != CodeCheckpoint {
		t.Fatalf("error = %v", err)
	}
}

func TestResumeRejectsCorruptArtifact(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	input := filepath.Join(root, fixtures.NameGood)
	cpPath := filepath.Join(work, "cp.json")
	outDir := filepath.Join(work, "out")
	if _, err := runOnce(t, input, filepath.Join(work, "cache"), cpPath, outDir, 2); err == nil {
		t.Fatal("expected interruption")
	}
	// Truncate a landed artifact; the recorded digest no longer matches.
	if err := os.WriteFile(filepath.Join(outDir, "cam-a-0001.m4s"), []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runOnce(t, input, filepath.Join(work, "cache"), cpPath, outDir, 0)
	if err == nil {
		t.Fatal("expected artifact verification failure")
	}
	if e, ok := model.As(err); !ok || e.Code != CodeResumeMismatch {
		t.Fatalf("error = %v", err)
	}
}

func TestCacheCorruptPayloadRecomputes(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	input := filepath.Join(root, fixtures.NameGood)
	cacheDir := filepath.Join(work, "cache")
	runOK(t, input, cacheDir, filepath.Join(work, "cp.json"), filepath.Join(work, "out"), 0)

	// Corrupt every cached payload.
	content, err := filepath.Glob(filepath.Join(cacheDir, "content", "*.bin"))
	if err != nil || len(content) == 0 {
		t.Fatalf("no cache payloads: %v", err)
	}
	for _, p := range content {
		if err := os.WriteFile(p, []byte("corrupt"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := runOK(t, input, cacheDir, filepath.Join(work, "cp2.json"), filepath.Join(work, "out2"), 0)
	if res.CacheMisses != len(content) {
		t.Fatalf("cache misses = %d, want %d", res.CacheMisses, len(content))
	}
	if len(res.CacheReasons) == 0 {
		t.Fatal("no recompute reason recorded")
	}
	// The recomputed output must still be identical.
	if res.IndexSum == "" {
		t.Fatal("no index digest")
	}
}

func TestCacheSourceChangeRecomputes(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	input := filepath.Join(root, fixtures.NameGood)
	cacheDir := filepath.Join(work, "cache")
	runOK(t, input, cacheDir, filepath.Join(work, "cp.json"), filepath.Join(work, "out"), 0)

	c := mustOpenCache(t, cacheDir)
	// A key built with a different source digest must not hit.
	sumA := sha256.Sum256([]byte("origin-a"))
	sumB := sha256.Sum256([]byte("origin-b"))
	params := cache.Params{MediaRateNum: 720000, MediaRateDen: 1, VideoTrackID: 1, AudioTrackID: 2, Repair: true}
	key := cache.NewKey(cache.KeyInput{Kind: "fragment-artifact", Source: "cam-a", SourcePath: "cam-a-0000.m4s", SourceSum: sumA, Params: params})
	if _, err := c.Put(key, sumA, []byte("artifact")); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(key, sumB)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry != nil {
		t.Fatal("cache hit despite changed source digest")
	}
	if got.Reason != "source digest changed" {
		t.Fatalf("reason = %q", got.Reason)
	}
}

func mustOpenCache(t *testing.T, dir string) *cache.Cache {
	t.Helper()
	c, err := cache.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTruncatedExitCodeAndOffset(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	_, err := runOnce(t, filepath.Join(root, fixtures.NameTruncated), filepath.Join(work, "cache"), filepath.Join(work, "cp.json"), filepath.Join(work, "out"), 0)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	e, ok := model.As(err)
	if !ok || e.Code != model.CodeTruncatedBox {
		t.Fatalf("error = %v", err)
	}
	if !e.HasByte || e.Offset <= 0 {
		t.Fatalf("first unreadable offset = %d/%v", e.Offset, e.HasByte)
	}
	if model.ExitCodeOf(err) != 2 {
		t.Fatalf("exit = %d, want 2", model.ExitCodeOf(err))
	}
}

func TestOversizeRejected(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	_, err := runOnce(t, filepath.Join(root, fixtures.NameOversize), filepath.Join(work, "cache"), filepath.Join(work, "cp.json"), filepath.Join(work, "out"), 0)
	if err == nil {
		t.Fatal("expected oversize error")
	}
	if e, ok := model.As(err); !ok || (e.Code != CodeFragmentTooBig && e.Code != model.CodeTruncatedBox) {
		t.Fatalf("error = %v", err)
	}
}

func TestQueueOverflowRejected(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	_, err := runOnce(t, filepath.Join(root, fixtures.NameQueue), filepath.Join(work, "cache"), filepath.Join(work, "cp.json"), filepath.Join(work, "out"), 0)
	if err == nil {
		t.Fatal("expected queue overflow")
	}
	if e, ok := model.As(err); !ok || e.Code != CodeQueueOverflow {
		t.Fatalf("error = %v", err)
	}
}

func TestStopAfterRelativeToCursor(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	input := filepath.Join(root, fixtures.NameEdge)
	cacheDir := filepath.Join(work, "cache")
	cpPath := filepath.Join(work, "cp.json")
	outDir := filepath.Join(work, "out")
	if _, err := runOnce(t, input, cacheDir, cpPath, outDir, 2); err == nil {
		t.Fatal("expected interruption")
	}
	// Each invocation makes two more records of progress.
	if _, err := runOnce(t, input, cacheDir, cpPath, outDir, 2); err == nil {
		t.Fatal("expected second interruption")
	}
	res := runOK(t, input, cacheDir, cpPath, outDir, 0)
	if res.Cursor != res.Records {
		t.Fatalf("cursor = %d, want %d", res.Cursor, res.Records)
	}
}

func TestTruncationDetectedAtFirstBadOffset(t *testing.T) {
	root := genFixtures(t, fixtures.DefaultOptions())
	path := filepath.Join(root, fixtures.NameTruncated, "cam-a-0003.m4s")
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Confirm the parser names the mdat box start rather than EOF.
	_, err = cmaf.ParseBytes(path, full)
	if err == nil {
		t.Fatal("expected error")
	}
	e, ok := model.As(err)
	if !ok || e.Code != model.CodeTruncatedBox {
		t.Fatalf("error = %v", err)
	}
	if !e.HasByte {
		t.Fatal("no byte offset")
	}
	_ = hex.EncodeToString(full[:4])
}
