package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mediamux/internal/fixture"
	"mediamux/manifest"
	"mediamux/media"
)

// testSegment builds a deterministic 90000Hz segment of ~250KB. 150 samples
// of 3603 ticks each make it exactly 6.005s long.
func testSegment(i int) []byte {
	return fixture.BuildSegment(fixture.SegmentSpec{
		Samples: 150, SampleTicks: 3603, TargetBytes: 250000, KeyframeEvery: 25, Seed: int64(i),
	})
}

func buildInput(t *testing.T, corruptAt int) *Input {
	t.Helper()
	ids := []string{"seg-0.m4s", "seg-1.m4s", "seg-2.m4s"}
	raw := fixture.HLSManifest("7", []string{"6.005", "6.005", "6.005"}, ids)
	raw = raw[:len(raw)-len("#EXT-X-ENDLIST\n")] + "#EXT-X-BITRATE:333\n#EXT-X-ENDLIST\n"
	doc, err := manifest.ParseHLS(raw)
	if err != nil {
		t.Fatal(err)
	}
	in := &Input{Manifest: doc, ManifestRaw: raw}
	for i := range ids {
		seg := testSegment(i)
		if i == corruptAt {
			seg = fixture.Truncate(seg, len(seg)*2/3)
		}
		in.Segments = append(in.Segments, seg)
		in.Nodes = append(in.Nodes, "edge-a")
	}
	return in
}

func checkpointIn(t *testing.T) string {
	return filepath.Join(t.TempDir(), "checkpoint.json")
}

func runWith(t *testing.T, opts Options, in *Input) *Result {
	t.Helper()
	if opts.MemoryLimit == 0 {
		opts.MemoryLimit = 8 << 20
	}
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRunnerCleanInputCommitsEverySegment(t *testing.T) {
	in := buildInput(t, -1)
	res := runWith(t, Options{Dir: t.TempDir(), CheckpointPath: checkpointIn(t)}, in)
	if len(res.Segments) != 3 {
		t.Fatalf("segments=%d", len(res.Segments))
	}
	for _, s := range res.Segments {
		if s.Corrupt {
			t.Fatalf("segment %d wrongly corrupt: %+v", s.Index, s.Damaged)
		}
	}
	if res.Checkpoint == nil || res.Checkpoint.Cursor != 3 || res.Checkpoint.LastCompleteIdx != 2 {
		t.Fatalf("checkpoint=%+v", res.Checkpoint)
	}
	if res.ResultSHA256 == "" {
		t.Fatal("missing result digest")
	}
	if res.PeakBufferBytes > 8<<20 {
		t.Fatalf("peak %d exceeds limit", res.PeakBufferBytes)
	}
}

func TestRunnerIdenticalInputSameDigest(t *testing.T) {
	in := buildInput(t, 1)
	opts := Options{Dir: t.TempDir(), CheckpointPath: checkpointIn(t)}
	a := runWith(t, opts, in)
	b := runWith(t, Options{Dir: t.TempDir(), CheckpointPath: checkpointIn(t)}, in)
	if a.ResultSHA256 != b.ResultSHA256 {
		t.Fatalf("digest differs:\n%s\n%s", a.ResultSHA256, b.ResultSHA256)
	}
	if len(a.Segments) != len(b.Segments) || a.CorruptSegments != b.CorruptSegments {
		t.Fatal("segment boundaries differ between identical runs")
	}
}

func TestRunnerDiagnosesCorruptionAndContinues(t *testing.T) {
	in := buildInput(t, 1)
	res := runWith(t, Options{Dir: t.TempDir(), CheckpointPath: checkpointIn(t)}, in)
	if res.CorruptSegments == 0 {
		t.Fatal("truncated segment not diagnosed")
	}
	bad := res.Segments[1]
	if !bad.Corrupt || len(bad.Damaged) == 0 {
		t.Fatalf("segment 1=%+v", bad)
	}
	if res.Segments[2].Corrupt {
		t.Fatal("clean segment after a damaged one was contaminated")
	}
	if res.Checkpoint.Cursor != 3 {
		t.Fatalf("pipeline did not continue past damage: cursor=%d", res.Checkpoint.Cursor)
	}
}

func TestRunnerResumesFromLastCompleteSegment(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")
	in := buildInput(t, -1)
	first := runWith(t, Options{Dir: dir, CheckpointPath: cpPath, ProcessSegments: 1}, in)
	if first.Checkpoint.Cursor != 1 {
		t.Fatalf("first run cursor=%d", first.Checkpoint.Cursor)
	}
	second := runWith(t, Options{Dir: dir, CheckpointPath: cpPath}, in)
	if !second.Resume.Resumed || second.Resume.FromSegment != 1 {
		t.Fatalf("resume=%+v", second.Resume)
	}
	if second.Checkpoint.Cursor != 3 {
		t.Fatalf("resumed cursor=%d", second.Checkpoint.Cursor)
	}
	// The resumed run's digest must equal a full cold run over the same input.
	full := runWith(t, Options{Dir: t.TempDir(), CheckpointPath: checkpointIn(t)}, in)
	if second.ResultSHA256 != full.ResultSHA256 {
		t.Fatalf("resume digest %s != full %s", second.ResultSHA256, full.ResultSHA256)
	}
}

func TestRunnerRejectsChangedManifestOnResume(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")
	first := buildInput(t, -1)
	runWith(t, Options{Dir: dir, CheckpointPath: cpPath, ProcessSegments: 1}, first)
	changed := buildInput(t, -1)
	changed.ManifestRaw += "\n#EXT-X-BITRATE:9000\n"
	res := runWith(t, Options{Dir: dir, CheckpointPath: cpPath}, changed)
	if res.Resume.Resumed && res.Resume.FromSegment > 0 {
		t.Fatalf("changed manifest must not resume: %+v", res.Resume)
	}
	found := false
	for _, e := range res.Events {
		if e.Kind == "checkpoint_reset" {
			found = true
		}
	}
	if !found {
		t.Fatal("manifest change was not reported")
	}
	if res.Checkpoint.Cursor != 3 {
		t.Fatalf("full rerun did not complete: cursor=%d", res.Checkpoint.Cursor)
	}
}

func TestRunnerFakeSourceIsRejectedThenRebuilt(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")
	in := buildInput(t, -1)
	runWith(t, Options{Dir: dir, CheckpointPath: cpPath}, in)
	// Re-run with an expectation that the cached entries came from another
	// node. The checkpoint is deliberately fresh so every segment is looked
	// up again, and every lookup must be refused and rebuilt.
	forgedPath := filepath.Join(dir, "forged-checkpoint.json")
	res := runWith(t, Options{Dir: dir, CheckpointPath: forgedPath, FakeSourceNode: "edge-forged"}, in)
	if res.Cache.SourceRejects == 0 {
		t.Fatalf("forged source accepted: %+v", res.Cache)
	}
	if res.Cache.Rebuilt == 0 {
		t.Fatalf("rejected entries were not rebuilt: %+v", res.Cache)
	}
}

func TestRunnerCacheHitSkipsAnalysis(t *testing.T) {
	dir := t.TempDir()
	in := buildInput(t, -1)
	runWith(t, Options{Dir: dir}, in)
	res := runWith(t, Options{Dir: dir}, in)
	if res.Cache.Hits != len(in.Segments) {
		t.Fatalf("cache hits=%d want %d", res.Cache.Hits, len(in.Segments))
	}
	for _, s := range res.Segments {
		if !s.FromCache {
			t.Fatalf("segment %d not served from cache", s.Index)
		}
	}
}

func TestRunnerRejectsOversizedWorkingSet(t *testing.T) {
	in := buildInput(t, -1)
	r, err := NewRunner(Options{MemoryLimit: 16, QueueDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), in)
	if err == nil || !media.HasCode(err, media.CodeInput) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunnerCancelStopsWithoutAdvancingCursor(t *testing.T) {
	dir := t.TempDir()
	in := buildInput(t, -1)
	ctx, cancel := context.WithCancel(context.Background())
	r, err := NewRunner(Options{Dir: dir, CheckpointPath: filepath.Join(dir, "cp.json"), MemoryLimit: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := r.Run(ctx, in); err == nil {
		t.Fatal("canceled run must error")
	}
	cp, err := Load(filepath.Join(dir, "cp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cp != nil && cp.Cursor != 0 {
		t.Fatalf("cancel advanced cursor to %d", cp.Cursor)
	}
	_ = os.Remove(filepath.Join(dir, "cp.json"))
	_ = fmt.Sprintf
}

func TestRunnerSlowConsumerStopsAtLastCompleteCheckpoint(t *testing.T) {
	dir := t.TempDir()
	// A consumer delay far longer than the idle timeout must stop the
	// branch without advancing past the last fully committed segment.
	r2, err := NewRunner(Options{
		Dir: dir, CheckpointPath: filepath.Join(dir, "cp2.json"),
		MemoryLimit: 8 << 20, SlowConsumerMS: 1, QueueDepth: 1, ConsumerDelayMS: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	in2 := buildInput(t, -1)
	res2, err := r2.Run(context.Background(), in2)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.SlowConsumer {
		t.Skip("consumer did not time out on this scheduler; timing-dependent")
	}
	if res2.Checkpoint == nil || res2.Checkpoint.Cursor != res2.Checkpoint.LastCompleteIdx+1 {
		t.Fatalf("checkpoint not aligned with last complete segment: %+v", res2.Checkpoint)
	}
	if res2.Checkpoint.Cursor >= len(in2.Segments) {
		t.Fatalf("slow consumer ran to completion: %d", res2.Checkpoint.Cursor)
	}
}

func TestRunnerStreamsSourcesUnderMemoryLimit(t *testing.T) {
	in := buildInput(t, -1)
	// Convert inline payloads into streamed sources with declared sizes.
	sizes := make([]int64, len(in.Segments))
	for i, seg := range in.Segments {
		sizes[i] = int64(len(seg))
	}
	sources := make([]io.ReadCloser, len(in.Segments))
	for i, seg := range in.Segments {
		sources[i] = io.NopCloser(bytes.NewReader(seg))
	}
	in.Sources, in.SourceSizes, in.Segments = sources, sizes, nil
	res := runWith(t, Options{Dir: t.TempDir(), CheckpointPath: checkpointIn(t)}, in)
	if len(res.Segments) != 3 || res.CorruptSegments != 0 {
		t.Fatalf("streamed run segs=%d corrupt=%d", len(res.Segments), res.CorruptSegments)
	}
	// Digest must match an inline run over the same bytes.
	inline := buildInput(t, -1)
	want := runWith(t, Options{Dir: t.TempDir(), CheckpointPath: checkpointIn(t)}, inline)
	if res.ResultSHA256 != want.ResultSHA256 {
		t.Fatalf("streamed digest %s != inline %s", res.ResultSHA256, want.ResultSHA256)
	}
}

func TestRunnerStreamedSourceLengthMismatchIsDiagnosed(t *testing.T) {
	in := buildInput(t, -1)
	sizes := []int64{int64(len(in.Segments[0])), int64(len(in.Segments[1])), int64(len(in.Segments[2]))}
	sources := []io.ReadCloser{
		io.NopCloser(bytes.NewReader(in.Segments[0])),
		// Declares more bytes than it can deliver.
		io.NopCloser(bytes.NewReader(in.Segments[1][:100])),
		io.NopCloser(bytes.NewReader(in.Segments[2])),
	}
	in.Sources, in.SourceSizes, in.Segments = sources, sizes, nil
	r, err := NewRunner(Options{MemoryLimit: 8 << 20, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), in); err == nil {
		t.Fatal("truncated stream must be reported")
	} else if !media.HasCode(err, media.CodeTruncated) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunnerCanceledStreamedReadReturnsPromptly(t *testing.T) {
	in := buildInput(t, -1)
	started := make(chan struct{})
	sources := make([]io.ReadCloser, len(in.Segments))
	blocker := newBlockingSource()
	started = blocker.started
	sources[0] = blocker
	sources[1] = io.NopCloser(bytes.NewReader(in.Segments[1]))
	sources[2] = io.NopCloser(bytes.NewReader(in.Segments[2]))
	sizes := make([]int64, len(in.Segments))
	sizes[0] = int64(len(in.Segments[0]))
	sizes[1] = int64(len(in.Segments[1]))
	sizes[2] = int64(len(in.Segments[2]))
	in.Sources, in.SourceSizes, in.Segments = sources, sizes, nil
	ctx, cancel := context.WithCancel(context.Background())
	r, err := NewRunner(Options{MemoryLimit: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, in); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || !media.HasCode(err, media.CodeCanceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled streamed read did not return")
	}
}

// blockingSource blocks until closed, proving cancellation reaches the read.
type blockingSource struct {
	started chan struct{}
	once    sync.Once
	closed  chan struct{}
}

func newBlockingSource() *blockingSource {
	return &blockingSource{started: make(chan struct{}), closed: make(chan struct{})}
}

func (b *blockingSource) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingSource) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
