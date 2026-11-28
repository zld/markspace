package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archive-lab/internal/fixture"
	"archive-lab/internal/manifest/scan"
	"archive-lab/internal/media"
	"archive-lab/internal/quality"
)

func setup(t *testing.T) (dir string, opts Options) {
	t.Helper()
	dir = t.TempDir()
	if _, err := fixture.Generate(dir); err != nil {
		t.Fatal(err)
	}
	opts = Options{
		InputPath:   filepath.Join(dir, "normal.m3u8"),
		InputRoot:   dir,
		IndexPath:   filepath.Join(dir, ".archive-lab", "index.json"),
		CacheDir:    filepath.Join(dir, ".archive-lab", "cache"),
		ArtifactDir: filepath.Join(dir, ".archive-lab", "out"),
		Request:     quality.Request{TargetBps: 800_000, MaxBps: 900_000, MinPSNR: 35, BudgetMs: 5000},
		Limits:      DefaultTestLimits(),
	}
	return dir, opts
}

// DefaultTestLimits keeps scans bounded in tests.
func DefaultTestLimits() scan.Limits {
	return scan.Limits{
		MaxSegmentBytes:    8 << 20,
		MaxTotalBytes:      64 << 20,
		MaxFiles:           100,
		ValidateContainers: true,
	}
}

func runOnce(t *testing.T, r *Runner, opts Options) *Report {
	t.Helper()
	res := r.Run(opts, nil)
	if res.Report == nil {
		t.Fatal("no report")
	}
	return res.Report
}

func TestRunNormalMeetsAcceptance(t *testing.T) {
	dir, opts := setup(t)
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	rep := runOnce(t, r, opts)
	if rep.Status != "ok" {
		t.Fatalf("status = %s (%s)", rep.Status, rep.Error)
	}
	if rep.SegmentsDone != fixture.SegmentCount {
		t.Fatalf("segments done = %d", rep.SegmentsDone)
	}
	if rep.PSNRDB == nil || *rep.PSNRDB < 35 {
		t.Fatalf("PSNR missing or below 35: %v", rep.PSNRDB)
	}
	if absFloat(rep.BitrateErrorPct) > 5 {
		t.Fatalf("bitrate error %.2f%% exceeds 5%%", rep.BitrateErrorPct)
	}
	if rep.PlaylistDriftMS > 1 {
		t.Fatalf("drift %.3f ms exceeds 1 ms", rep.PlaylistDriftMS)
	}
	if rep.InputSHA256 == "" || rep.ConfigSHA256 == "" {
		t.Fatal("digests missing")
	}
	if rep.Cache != "miss" || !rep.IndexRebuilt {
		t.Fatalf("first run should be a miss with rebuild, got cache=%s rebuilt=%v", rep.Cache, rep.IndexRebuilt)
	}
	if rep.RTPPackets != fixtureFramesPerSegmentRTP() {
		t.Fatalf("rtp packets = %d", rep.RTPPackets)
	}
	_ = dir
}

func fixtureFramesPerSegmentRTP() int { return 4 }

func TestRunRepeatIsCacheHitAndDeterministic(t *testing.T) {
	_, opts := setup(t)
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	first := runOnce(t, r, opts)
	second := runOnce(t, r, opts)
	if second.Cache != "hit" {
		t.Fatalf("repeat cache = %s", second.Cache)
	}
	if second.IndexRebuilt {
		t.Fatal("repeat must not rebuild the index")
	}
	if first.Digest() != second.Digest() {
		t.Fatalf("report digest differs:\n%s\n%s", first.Digest(), second.Digest())
	}
}

func TestRunSamePathReplacementRebuildsAndMisses(t *testing.T) {
	dir, opts := setup(t)
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	runOnce(t, r, opts)
	// Replace normal segment bytes at the same path from the replace fixture.
	src, err := os.ReadFile(filepath.Join(dir, "media", "replace", media.Name(0)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "media", "normal", media.Name(0)), src, 0o644); err != nil {
		t.Fatal(err)
	}
	rep := runOnce(t, r, opts)
	if !rep.IndexRebuilt {
		t.Fatal("content change at same path must rebuild the index")
	}
	if rep.Cache != "miss" {
		t.Fatalf("content change must miss the cache, got %s", rep.Cache)
	}
}

func TestRunMetadataOnlyChangeRebuildsIndex(t *testing.T) {
	dir, opts := setup(t)
	meta := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(meta, []byte(`{"tool":"v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts.Metadata = []string{"meta.json"}
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	first := runOnce(t, r, opts)
	if first.Status != "ok" {
		t.Fatal(first.Error)
	}
	if err := os.WriteFile(meta, []byte(`{"tool":"v2"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	second := runOnce(t, r, opts)
	if !second.IndexRebuilt {
		t.Fatal("metadata update must rebuild the index")
	}
	if second.Cache != "miss" {
		t.Fatalf("metadata update must miss the cache, got %s", second.Cache)
	}
	if first.InputDigest == second.InputDigest {
		t.Fatal("input digest must change when metadata changes")
	}
}

func TestConfigChangeInvalidatesCacheButKeepsInputIndex(t *testing.T) {
	_, opts := setup(t)
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	first := runOnce(t, r, opts)
	opts.Request.TargetBps = 700_000
	opts.Request.MaxBps = 800_000
	second := runOnce(t, r, opts)
	if second.Cache != "miss" {
		t.Fatalf("config change must miss the cache, got %s", second.Cache)
	}
	if second.IndexRebuilt {
		t.Fatal("config change must not rebuild the input index")
	}
	if first.ConfigSHA256 == second.ConfigSHA256 {
		t.Fatal("config digest must change with the request")
	}
	// The index file must still be the same committed index.
	idx, err := os.ReadFile(opts.IndexPath)
	if err != nil || len(idx) == 0 {
		t.Fatalf("index was removed or corrupted: %v", err)
	}
}

func TestCorruptIndexFallsBackToFullScan(t *testing.T) {
	_, opts := setup(t)
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	runOnce(t, r, opts)
	if err := os.WriteFile(opts.IndexPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := runOnce(t, r, opts)
	if rep.Status != "ok" || !rep.IndexRebuilt {
		t.Fatalf("corrupt index should force a rebuild, got status=%s rebuilt=%v", rep.Status, rep.IndexRebuilt)
	}
	// The rebuilt index must be readable again.
	if _, err := os.Stat(opts.IndexPath); err != nil {
		t.Fatalf("index not rewritten: %v", err)
	}
}

func TestTruncatedInputFailsWithoutIndexOrCache(t *testing.T) {
	dir, opts := setup(t)
	opts.InputPath = filepath.Join(dir, "truncated.m3u8")
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	rep := runOnce(t, r, opts)
	if rep.Status != "error" || rep.FailedStage != "parse" {
		t.Fatalf("expected parse failure, got status=%s stage=%s", rep.Status, rep.FailedStage)
	}
	if !strings.Contains(rep.Error, "truncated") && !strings.Contains(rep.Error, "mdat") {
		t.Fatalf("error lacks a location: %s", rep.Error)
	}
	if _, err := os.Stat(opts.IndexPath); !os.IsNotExist(err) {
		t.Fatal("no index may be committed for damaged input")
	}
	if _, err := os.Stat(opts.CacheDir); !os.IsNotExist(err) {
		t.Fatal("no cache entry may be written for damaged input")
	}
}

func TestMalformedManifestFailsWithoutIndex(t *testing.T) {
	dir, opts := setup(t)
	opts.InputPath = filepath.Join(dir, "malformed.mpd")
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	rep := runOnce(t, r, opts)
	if rep.Status != "error" || rep.FailedStage != "parse" {
		t.Fatalf("expected parse error, got %s/%s", rep.Status, rep.FailedStage)
	}
	if !strings.Contains(rep.Error, "malformed.mpd@") {
		t.Fatalf("error should carry a byte offset: %s", rep.Error)
	}
	if _, err := os.Stat(opts.IndexPath); !os.IsNotExist(err) {
		t.Fatal("no index may be committed for a damaged manifest")
	}
}

func TestCancellationRecordsProgressAndResumes(t *testing.T) {
	_, opts := setup(t)
	opts.ProgressPath = filepath.Join(opts.ArtifactDir, "progress.json")
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	calls := 0
	res := r.Run(opts, func() bool {
		calls++
		return calls > 2 // cancel before the third segment
	})
	if res.Error == nil {
		t.Fatal("expected cancellation error")
	}
	if res.Report.SegmentsDone != 2 {
		t.Fatalf("completed segments = %d, want 2", res.Report.SegmentsDone)
	}
	if res.Report.Status != "error" || res.Report.FailedStage != "execute" {
		t.Fatalf("status = %s stage = %s", res.Report.Status, res.Report.FailedStage)
	}
	// Rerun without cancellation: resume at segment 2 and finish.
	res2 := r.Run(opts, nil)
	if res2.Error != nil {
		t.Fatal(res2.Error)
	}
	if res2.Report.ResumedFrom != 2 {
		t.Fatalf("resumed from %d, want 2", res2.Report.ResumedFrom)
	}
	if res2.Report.SegmentsDone != fixture.SegmentCount {
		t.Fatalf("final segments = %d", res2.Report.SegmentsDone)
	}
}

func TestReportJSONShape(t *testing.T) {
	_, opts := setup(t)
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	rep := runOnce(t, r, opts)
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"target_bps", "actual_bps", "bitrate_error_pct", "psnr_db",
		"playlist_drift_ms", "input_sha256", "config_sha256", "cache", "index_rebuilt",
	} {
		if _, ok := m[key]; !ok {
			t.Fatalf("report missing %q", key)
		}
	}
}

func TestSegmentTimingMismatchStopsRun(t *testing.T) {
	dir, opts := setup(t)
	// Rewrite one segment's playlist duration so the container timing no
	// longer agrees with the timeline beyond the 1 ms budget.
	body := mustRead(t, opts.InputPath)
	broken := strings.Replace(string(body), "#EXTINF:1.000,\nmedia/normal/normal-0002.m4s", "#EXTINF:1.500,\nmedia/normal/normal-0002.m4s", 1)
	if broken == string(body) {
		t.Fatal("fixture manifest shape changed")
	}
	if err := os.WriteFile(opts.InputPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	res := r.Run(opts, nil)
	if res.Error == nil {
		t.Fatal("timing disagreement must stop the run")
	}
	if res.Report.FailedStage != "execute" {
		t.Fatalf("failed stage = %s", res.Report.FailedStage)
	}
	if !strings.Contains(res.Error.Error(), "segment 2") {
		t.Fatalf("error should identify the segment: %v", res.Error)
	}
	if res.Report.SegmentsDone != 2 {
		t.Fatalf("completed = %d, want 2", res.Report.SegmentsDone)
	}
	_ = dir
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunDASHAndMasterPlaylists(t *testing.T) {
	dir, opts := setup(t)
	r := &Runner{Cache: &Cache{Dir: opts.CacheDir}}
	for _, name := range []string{"normal.mpd", "master.m3u8"} {
		opts.InputPath = filepath.Join(dir, name)
		opts.IndexPath = filepath.Join(dir, ".archive-lab", name+".index.json")
		rep := runOnce(t, r, opts)
		if rep.Status != "ok" {
			t.Fatalf("%s: status=%s error=%s", name, rep.Status, rep.Error)
		}
		if rep.SegmentsDone != fixture.SegmentCount {
			t.Fatalf("%s: segments=%d", name, rep.SegmentsDone)
		}
		if rep.PSNRDB == nil || *rep.PSNRDB < 35 {
			t.Fatalf("%s: PSNR = %v", name, rep.PSNRDB)
		}
	}
}

func TestUnmeasurableSourceNeverFabricatesPSNR(t *testing.T) {
	// A compressed (non-raw) segment must leave psnr_db null and explain why.
	dir := t.TempDir()
	if _, err := fixture.Generate(dir); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		InputPath: filepath.Join(dir, "normal.m3u8"),
		InputRoot: dir,
		IndexPath: filepath.Join(dir, "idx.json"),
		CacheDir:  filepath.Join(dir, "cache"),
		Request:   quality.Request{TargetBps: 800_000, MaxBps: 900_000, MinPSNR: 35, BudgetMs: 5000},
	}
	sample := quality.Sample{}
	plan, err := quality.EstimatePlan(opts.Request, sample, 4, fixture.SegmentNanos)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PSNRKnown || len(plan.Unmet) == 0 {
		t.Fatalf("unmeasurable plan must say so: %+v", plan)
	}
}
