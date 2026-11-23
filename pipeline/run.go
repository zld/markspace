package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/zld/media-check/media"
)

// RunOptions configures a full production/consumption run.
type RunOptions struct {
	Source     string              // source MP4/MOV path
	OutDir     string              // all temp data and outputs live below this
	Workers    int                 // bounded pool size
	Queue      int                 // bounded queue capacity
	SamplesPer int                 // samples per segment
	Targets    []media.TrackTarget // target track layout
	Seed       int64
	Resume     bool       // reuse verified artifacts with matching hashes
	PoolCtl    PoolLimits // optional adaptive limits
}

// PoolLimits expresses the adaptively derived bounds; zero means "use opts".
type PoolLimits struct {
	Parallel int
	Queue    int
}

// RunReport is the complete result of a run.
type RunReport struct {
	Probe        *media.Probe    `json:"-"`
	TrackMap     *media.TrackMap `json:"-"`
	Map          *Manifest       `json:"manifest"`
	Segments     []media.Segment `json:"segments"`
	ManifestPath string          `json:"manifest_path"`
	ArtifactsDir string          `json:"artifacts_dir"`
	Cancelled    bool            `json:"cancelled"`
}

// Run executes the full pipeline: probe, map, split, hash/segment through a
// bounded pool, merge deterministically, extract keyframes and write the
// manifest atomically. A cancelled context stops producers, workers and output
// writers and the manifest records the cancelled tasks.
func Run(ctx context.Context, opts RunOptions) (*RunReport, error) {
	if opts.OutDir == "" {
		return nil, media.NewError(media.ClassInvalidArgument, "run", 0, "out dir is required")
	}
	if opts.SamplesPer <= 0 || opts.SamplesPer > 1_000_000 {
		return nil, media.NewError(media.ClassInvalidArgument, "run", 0, "samples per segment %d outside 1..1000000", opts.SamplesPer)
	}
	absSource, err := filepath.Abs(opts.Source)
	if err != nil {
		return nil, media.WrapError(media.ClassInvalidArgument, "run.source", 0, err, "bad source path")
	}
	absOut, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return nil, media.WrapError(media.ClassInvalidArgument, "run.out", 0, err, "bad output dir")
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return nil, media.WrapError(media.ClassIO, "run.mkdir", 0, err, "create output dir")
	}
	artifactsDir := filepath.Join(absOut, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return nil, media.WrapError(media.ClassIO, "run.mkdir", 0, err, "create artifacts dir")
	}
	// Path escape check: every directory we write into must live under the
	// requested output root.
	for _, p := range []string{artifactsDir} {
		if _, err := media.ValidateOutPath(absOut, p); err != nil {
			return nil, err
		}
	}

	src, err := os.Open(absSource)
	if err != nil {
		return nil, media.WrapError(media.ClassIO, "run.open", 0, err, "open source %q", absSource)
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return nil, media.WrapError(media.ClassIO, "run.stat", 0, err, "stat source")
	}

	probe, err := media.ProbeFile(src, fi.Size(), absSource)
	if err != nil {
		return nil, err
	}
	if err := media.ValidateMovieBounds(probe); err != nil {
		return nil, err
	}
	trmap, err := media.BuildTrackMap(probe, opts.Targets)
	if err != nil {
		return nil, err
	}
	tracks, err := trmap.MappedTracks(probe)
	if err != nil {
		return nil, err
	}
	segs, err := media.SplitSegments(tracks, opts.SamplesPer)
	if err != nil {
		return nil, err
	}
	srcSHA, err := media.HashFile(src)
	if err != nil {
		return nil, err
	}

	workers, queue := opts.Workers, opts.Queue
	if opts.PoolCtl.Parallel > 0 {
		workers = opts.PoolCtl.Parallel
	}
	if opts.PoolCtl.Queue > 0 {
		queue = opts.PoolCtl.Queue
	}

	merger, err := NewMerger(len(segs), absSource, srcSHA, opts.Seed)
	if err != nil {
		return nil, err
	}
	// A single read handle shared by workers is unsafe, so each job opens its
	// own descriptor lazily inside the handler.
	// A durable ledger of earlier verified segments lets a re-run skip work even
	// if the previous process died before writing its manifest.
	var ledger map[int]bool
	if opts.Resume {
		if entries, ok := ReadLedger(absOut, srcSHA); ok {
			ledger = ValidateLedger(absOut, entries)
		}
	}
	handler := makeHandler(absOut, artifactsDir, absSource, srcSHA, opts.Resume, ledger)
	pool, err := NewPool(ctx, workers, queue, handler)
	if err != nil {
		return nil, err
	}

	// Consumer goroutine merges results as they complete.
	consumerDone := make(chan struct{})
	var mergeErr error
	go func() {
		defer close(consumerDone)
		for r := range pool.Results() {
			if err := merger.Add(r); err != nil && mergeErr == nil {
				mergeErr = err
			}
		}
	}()

	// Producer: feed jobs, respecting backpressure and cancellation.
	produceErr := produce(ctx, pool, tracks, segs, trmap, absSource)
	// Only a failed or cancelled producer aborts work in flight; a completed
	// producer lets every queued job finish.
	if produceErr != nil || ctx.Err() != nil {
		pool.Cancel()
	}
	pool.Close()
	<-consumerDone

	stats := pool.Stats()
	merger.SetStats(stats)
	if mergeErr != nil {
		return nil, mergeErr
	}
	if produceErr != nil && !errors.Is(produceErr, context.Canceled) {
		return nil, produceErr
	}

	_, idxReport, err := buildIndexReportWithRetry(src, probe, trmap, segs, opts.Seed)
	if err != nil && ctx.Err() == nil {
		return nil, err
	}
	man, err := merger.Build(Manifest{Tracks: len(tracks), Index: idxReport})
	if err != nil {
		return nil, err
	}
	// A cancelled run legitimately has missing segments, so count conservation
	// only applies to completed runs; structural invariants always apply.
	expected := expectedCounts(tracks)
	if ctx.Err() != nil {
		expected = nil
	}
	if err := man.Invariants(expected); err != nil {
		return nil, err
	}
	man.SHA256, _ = man.Digest()

	manPath := filepath.Join(absOut, "manifest.json")
	if err := writeJSONAtomic(absOut, manPath, man); err != nil {
		return nil, err
	}
	if err := WriteLedger(absOut, srcSHA, man); err != nil {
		return nil, err
	}
	return &RunReport{
		Probe: probe, TrackMap: trmap, Map: man, Segments: segs,
		ManifestPath: manPath, ArtifactsDir: artifactsDir,
		Cancelled: ctx.Err() != nil,
	}, nil
}

func expectedCounts(tracks []*media.TrackInfo) map[uint32]uint32 {
	out := make(map[uint32]uint32, len(tracks))
	for _, t := range tracks {
		out[t.TrackID] = t.SampleCount
	}
	return out
}

// produce submits every segment, blocking on a full queue and stopping promptly
// on cancellation. It never submits after the context is done.
func produce(ctx context.Context, pool *Pool, tracks []*media.TrackInfo, segs []media.Segment, m *media.TrackMap, source string) error {
	byID := make(map[uint32]*media.TrackInfo, len(tracks))
	for _, t := range tracks {
		byID[t.TrackID] = t
	}
	for _, seg := range segs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ti := byID[seg.TrackID]
		job := Job{
			Index: seg.Index, TrackID: seg.TrackID, Segment: seg,
			Table: ti.Table, SourcePath: source,
		}
		if err := pool.Submit(job); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrPoolClosed) {
				return context.Canceled
			}
			return err
		}
	}
	return nil
}

// buildIndexReportWithRetry builds the keyframe index and validates a random
// sample of hits, returning the report for the manifest.
func buildIndexReportWithRetry(src io.ReadSeeker, p *media.Probe, m *media.TrackMap, segs []media.Segment, seed int64) (*media.Index, *IndexReport, error) {
	tracks, err := m.MappedTracks(p)
	if err != nil {
		return nil, nil, err
	}
	// The index is verified against the source file the segments were hashed
	// from; this is the same content an archive reader would seek into.
	idx, err := media.BuildIndex(tracks, segs)
	if err != nil {
		return nil, nil, err
	}
	checks := 32
	if idx.Len() < checks {
		checks = idx.Len()
	}
	ratio, _, err := idx.VerifyRandom(src, tracks, seed, checks)
	if err != nil {
		return nil, nil, err
	}
	return idx, &IndexReport{SHA: idx.SHA(), Keyframes: idx.Len(), HitRatio: ratio, Checks: checks}, nil
}

// Reprobe re-opens a path and returns its probe; used by verify and tests.
func Reprobe(path string) (*media.Probe, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, media.WrapError(media.ClassIO, "reprobe.open", 0, err, "open %q", path)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, media.WrapError(media.ClassIO, "reprobe.stat", 0, err, "stat %q", path)
	}
	p, err := media.ProbeFile(f, fi.Size(), path)
	if err != nil {
		return nil, fi.Size(), err
	}
	return p, fi.Size(), nil
}
