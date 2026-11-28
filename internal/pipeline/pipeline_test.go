package pipeline_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"rtptrace/internal/container"
	"rtptrace/internal/fault"
	"rtptrace/internal/fixture"
	"rtptrace/internal/pipeline"
	"rtptrace/internal/stage"
)

func specs() []fixture.TrackSpec {
	return []fixture.TrackSpec{
		{TrackID: 1, Timescale: 48000, SampleCount: 40, SampleSize: 20,
			SamplesPerChunk: 8, Delta: 480, SSRC: 0x4000, PayloadType: 96},
		{TrackID: 2, Timescale: 90000, SampleCount: 28, SampleSize: 32,
			SamplesPerChunk: 4, Delta: 3000, SSRC: 0x4001, PayloadType: 97},
	}
}

func cfg(input string, seed int64, interrupt string) pipeline.Config {
	return pipeline.Config{
		InputPath:      input,
		Seed:           seed,
		WindowCapacity: 4,
		ReorderEvery:   3,
		DuplicateEvery: 5,
		BurstLoss:      []int{12, 13, 14},
		StartSeq:       100,
		InterruptAfter: interrupt,
	}
}

// runOnce drives the pipeline against a fresh store and returns the merge
// bundle digest plus emitted RTP stats.
func runOnce(t *testing.T, root, input string, seed int64, interrupt string) (string, pipeline.RTPResult) {
	t.Helper()
	store, err := stage.OpenStore(root, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st, err := store.Ensure("digest", seed, stage.OrderedNames)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	r := stage.NewRunner(store, pipeline.Executors(cfg(input, seed, interrupt)))
	r.InterruptAfter = interrupt
	if _, err := r.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	if interrupt != "" {
		return "", pipeline.RTPResult{}
	}
	rec := st.Stages[stage.NameMerge]
	bundleBytes, err := store.ReadArtifact(rec)
	if err != nil {
		t.Fatalf("read merge: %v", err)
	}
	var bundle struct {
		Digest   string `json:"artifact_sha256"`
		Artifact string `json:"artifact_hex"`
	}
	if err := json.Unmarshal(bundleBytes, &bundle); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	rtpBytes, err := store.ReadArtifact(st.Stages[stage.NameRTP])
	if err != nil {
		t.Fatalf("read rtp: %v", err)
	}
	var rr pipeline.RTPResult
	if err := json.Unmarshal(rtpBytes, &rr); err != nil {
		t.Fatalf("rtp unmarshal: %v", err)
	}
	return bundle.Digest, rr
}

func TestResumedRunMatchesFullRunChecksum(t *testing.T) {
	dir := t.TempDir()
	gen, err := fixture.BuildMP4(filepath.Join(dir, "input"), 17, specs())
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// Baseline: uninterrupted full run.
	fullDigest, fullStats := runOnce(t, filepath.Join(dir, "full"), gen.Path, 17, "")

	// Interrupted then resumed run over a separate store but identical input.
	intRoot := filepath.Join(dir, "interrupted")
	runOnce(t, intRoot, gen.Path, 17, stage.NameRTP)
	resumedDigest, resumedStats := runOnce(t, intRoot, gen.Path, 17, "")

	if resumedDigest != fullDigest {
		t.Fatalf("artifact digest diverges after resume:\n full=%s\n resumed=%s",
			fullDigest, resumedDigest)
	}
	if resumedStats.Stats.LostPackets != fullStats.Stats.LostPackets ||
		resumedStats.Stats.Duplicates != fullStats.Stats.Duplicates {
		t.Fatalf("counters differ after resume: full=%+v resumed=%+v",
			fullStats.Stats, resumedStats.Stats)
	}
}

func TestTruncationLocatedWithinEightBytes(t *testing.T) {
	dir := t.TempDir()
	gen, err := fixture.BuildMP4(filepath.Join(dir, "src"), 17, []fixture.TrackSpec{
		{TrackID: 1, Timescale: 48000, SampleCount: 200, SampleSize: 20,
			SamplesPerChunk: 10, Delta: 480, SSRC: 0x4000},
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	const cut = int64(4096)
	dst := filepath.Join(dir, "cut.mp4")
	if err := fixture.TruncateCopy(gen.Path, dst, cut); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, err = container.ScanFile(dst)
	if err == nil {
		t.Fatal("expected corruption error")
	}
	fe, ok := fault.AsError(err)
	if !ok || fe.Kind != fault.KindCorrupt {
		t.Fatalf("want corrupt fault, got %v", err)
	}
	diff := fe.Offset - cut
	if diff < 0 {
		diff = -diff
	}
	if diff > 8 {
		t.Fatalf("bad_offset=%d differs from cut %d by %d (>8)", fe.Offset, cut, diff)
	}
}

func TestNoDoubleCountingOnRepeatedResume(t *testing.T) {
	dir := t.TempDir()
	gen, err := fixture.BuildMP4(filepath.Join(dir, "input"), 17, specs())
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	root := filepath.Join(dir, "run")
	first, stats1 := runOnce(t, root, gen.Path, 17, "")
	// Resuming a completed run must reuse checkpoints and not recount packets.
	second, stats2 := runOnce(t, root, gen.Path, 17, "")
	if first != second {
		t.Fatalf("digest changed on repeated resume: %s vs %s", first, second)
	}
	if stats1.Stats.LostPackets != stats2.Stats.LostPackets ||
		stats1.Stats.Duplicates != stats2.Stats.Duplicates {
		t.Fatalf("packets recounted across resume: %+v vs %+v", stats1.Stats, stats2.Stats)
	}
}
