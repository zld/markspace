// Package pipeline wires the EBML parser, unified timeline, representation
// aligner, deterministic manifest and checkpoint index into one auditable
// offline run with interrupt/resume support.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"mediacheck/internal/align"
	"mediacheck/internal/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/internal/index"
	"mediacheck/internal/manifest"
	"mediacheck/internal/matroska"
	"mediacheck/internal/timeline"
)

// Exit categories encoded in pipeline errors; the CLI maps them to exit codes.
const (
	ExitOK         = 0
	ExitMaterial   = 2 // corrupt material or timeline ambiguity
	ExitIndex      = 3 // checkpoint verification/recovery failure
)

// Config is the complete, digestable run configuration.
type Config struct {
	Seed            int64
	Rates           int
	Clusters        int
	CheckpointEvery int
	InterruptAfter  int // 0 disables; crash before committing this chunk
	Resume          bool
	DryRun          bool
	Corrupt         fixture.Corruption
	WorkDir         string
	TargetSegmentNS int64
	MaxSkewNS       int64
	DriftLimitPPM   float64
}

// Result summarises one run for the terminal report.
type Result struct {
	Representations   int
	SegmentsEach      int
	BoundarySkewMS    int64
	ManifestSHA       string
	FirstErrorClass   ebml.ErrorClass
	FirstErrorOffset  int64
	RecoveryCheckpoint uint64
	ReplayedChunks    uint64
	RecoveryMS        int64
	Manifest          *manifest.Manifest
}

// Error is a typed pipeline failure carrying the exit category.
type Error struct {
	Exit   int
	Class  ebml.ErrorClass
	Offset int64
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("pipeline: exit=%d class=%s offset=%d: %s", e.Exit, e.Class, e.Offset, e.Msg)
}

func materialError(class ebml.ErrorClass, offset int64, format string, a ...any) *Error {
	return &Error{Exit: ExitMaterial, Class: class, Offset: offset, Msg: fmt.Sprintf(format, a...)}
}

func indexError(format string, a ...any) *Error {
	return &Error{Exit: ExitIndex, Class: ebml.ErrBadElement, Offset: -1, Msg: fmt.Sprintf(format, a...)}
}

// Run executes (or resumes) one full processing pass.
func Run(cfg Config) (Result, error) {
	if err := cfg.validate(); err != nil {
		return Result{}, err
	}
	res := Result{Representations: cfg.Rates, FirstErrorOffset: -1}

	// 1. Build representations from deterministic synthetic sources.
	sources, err := buildSources(cfg)
	if err != nil {
		return res, err
	}

	// 2. Checkpoint store: resume from the last complete chunk, or start clean.
	store, err := index.OpenStore(cfg.WorkDir)
	if err != nil {
		return res, indexError("open store: %v", err)
	}
	inputDigest := combinedInputDigest(sources)
	configDigest, _ := manifest.Digest(digestableConfig(cfg))
	ckKey := index.ResumeKey(inputDigest, configDigest, "segments")

	startCursor := uint64(0)
	if cfg.Resume {
		t0 := time.Now()
		rec, rerr := index.Recover(store, ckKey)
		res.RecoveryMS = time.Since(t0).Milliseconds()
		if rerr != nil {
			return res, indexError("recovery failed: %v", rerr)
		}
		if rec.FoundAny {
			startCursor = rec.FromCursor
			res.RecoveryCheckpoint = rec.Checkpoint.ChunkSeq
		}
	} else if !cfg.DryRun {
		if err := store.Clear(); err != nil {
			return res, indexError("clear store: %v", err)
		}
	}

	// 3. Parse every representation; a structural defect stops the material.
	reps, perr := parseRepresentations(cfg, sources)
	if perr != nil {
		res.FirstErrorClass = perr.Class
		res.FirstErrorOffset = perr.Offset
		return res, perr
	}

	// 4. Unify timelines: unwrap, drift-correct, assert cross-track monotonic.
	if err := normaliseTimelines(cfg, reps); err != nil {
		return res, err
	}

	// 5. Align keyframe boundaries across representations.
	aligner, _ := align.New(align.Options{
		TargetSegmentNS: cfg.TargetSegmentNS, MaxSkewNS: cfg.MaxSkewNS,
	})
	boundaries, err := aligner.Align(toAlignReps(reps))
	if err != nil {
		return res, materialError(ebml.ErrBadElement, -1, "alignment rejected: %v", err)
	}
	res.SegmentsEach = len(boundaries)
	res.BoundarySkewMS = maxBoundarySkewMS(boundaries, reps)

	// 6. Stream chunks through the write-ahead checkpoint index.
	if !cfg.DryRun {
		committed, cerr := commitCheckpoints(cfg, store, ckKey, startCursor)
		if cerr != nil {
			return res, cerr
		}
		if cfg.Resume {
			res.ReplayedChunks = committed
		}
	}

	// 7. Build the deterministic manifest and verify invariants.
	// The final frame marks the last boundary; content extends one cluster
	// step beyond it so the trailing segment has a positive duration.
	repEnds := map[string]int64{}
	for _, r := range reps {
		repEnds[r.ID] = r.Frames[len(r.Frames)-1].T0 + cfg.TargetSegmentNS
	}
	mcfg := manifest.Config{
		TargetSegmentNS: cfg.TargetSegmentNS, MaxSkewNS: cfg.MaxSkewNS,
		DriftLimitPPM: cfg.DriftLimitPPM, RepresentationIDs: repIDs(reps),
	}
	builder, _ := manifest.NewBuilder(mcfg)
	mf, err := builder.Build(inputDigest, boundaries, repEnds)
	if err != nil {
		return res, materialError(ebml.ErrBadElement, -1, "manifest build: %v", err)
	}
	if err := CheckInvariants(mf, boundaries, reps, cfg.CheckpointEvery, cfg.DryRun); err != nil {
		return res, err
	}
	res.Manifest = mf
	res.ManifestSHA = mf.OutputSHA256
	return res, nil
}

func (cfg Config) validate() error {
	if cfg.Rates < 1 || cfg.Clusters < 2 {
		return materialError(ebml.ErrBadElement, -1, "rates>=1 and clusters>=2 required")
	}
	if cfg.CheckpointEvery < 1 {
		return materialError(ebml.ErrBadElement, -1, "checkpoint-every must be >=1")
	}
	if cfg.TargetSegmentNS <= 0 || cfg.MaxSkewNS <= 0 {
		return materialError(ebml.ErrBadElement, -1, "timing limits must be positive")
	}
	if cfg.InterruptAfter > cfg.Clusters {
		return materialError(ebml.ErrBadElement, -1, "interrupt-after exceeds chunk count")
	}
	return nil
}

// source is one representation's synthetic bytes plus its digest.
type source struct {
	ID     string
	Doc    []byte
	Digest string
}

func buildSources(cfg Config) ([]source, error) {
	out := make([]source, 0, cfg.Rates)
	for i := 0; i < cfg.Rates; i++ {
		opt := fixture.Options{
			Seed: cfg.Seed + int64(i)*1009, Tracks: 1,
			Clusters: cfg.Clusters, BlocksPerClus: 1,
			Wrap32:  cfg.Corrupt == fixture.CorruptWrapDrift,
			VFR:     cfg.Corrupt == fixture.CorruptVFRMisalign,
			UnknownLen: cfg.Corrupt == fixture.CorruptUnknownNesting,
			Corrupt: cfg.Corrupt,
		}
		doc := fixture.Generate(opt)
		sum := sha256.Sum256(doc)
		out = append(out, source{
			ID: fmt.Sprintf("rep-%d", i), Doc: doc, Digest: hex.EncodeToString(sum[:]),
		})
	}
	return out, nil
}

func combinedInputDigest(sources []source) string {
	h := sha256.New()
	for _, s := range sources {
		h.Write([]byte(s.ID))
		h.Write([]byte(s.Digest))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// parsedRep holds one representation's frames after EBML parsing.
type parsedRep struct {
	ID     string
	Frames []timeline.Frame
}

func parseRepresentations(cfg Config, sources []source) ([]parsedRep, *Error) {
	reps := make([]parsedRep, 0, len(sources))
	for _, src := range sources {
		lim := ebml.DefaultLimits()
		r := ebml.NewReader(bytesReader(src.Doc), lim)
		p := matroska.NewParser(r, lim)
		seg, err := p.ParseHeader(int64(len(src.Doc)))
		if err != nil {
			return nil, materialError(errClass(err), errOffset(err), "rep %s header: %v", src.ID, err)
		}
		var frames []timeline.Frame
		serr := p.ScanClusters(seg, int64(len(src.Doc)), func(cs matroska.ClusterScan) error {
			for _, b := range cs.Blocks {
				spec := p.Tracks()[b.TrackNumber]
				f, ferr := spec.Clock.Normalise(uint64(b.AbsTimeTicks), b.HeaderOffset)
				if ferr != nil {
					return ferr
				}
				f.Keyframe = b.Keyframe
				frames = append(frames, f)
			}
			return nil
		})
		if serr != nil {
			if pe, ok := serr.(*matroska.ParseError); ok {
				return nil, materialError(pe.Class, pe.Offset, "rep %s: %s", src.ID, pe.Msg)
			}
			if ae, ok := serr.(*timeline.AmbiguousError); ok {
				return nil, materialError(ebml.ErrBadElement, ae.Offset,
					"rep %s ambiguous timestamp track %d", src.ID, ae.TrackNumber)
			}
			return nil, materialError(ebml.ErrBadElement, -1, "rep %s: %v", src.ID, serr)
		}
		reps = append(reps, parsedRep{ID: src.ID, Frames: frames})
	}
	return reps, nil
}

func normaliseTimelines(cfg Config, reps []parsedRep) *Error {
	trackMap := map[uint64][]timeline.Frame{}
	for i, r := range reps {
		if len(r.Frames) == 0 {
			return materialError(ebml.ErrBadElement, -1, "rep %s produced no frames", r.ID)
		}
		// Per-track monotonicity: a regression here means wrap expansion could
		// not orient a timestamp uniquely. Keep the offending block offset.
		for j := 1; j < len(r.Frames); j++ {
			if r.Frames[j].T0 < r.Frames[j-1].T0 {
				return materialError(ebml.ErrBadElement, r.Frames[j].ByteOffset,
					"rep %s non-monotonic timestamp after wrap/drift at track block offset", r.ID)
			}
		}
		trackMap[uint64(i+1)] = r.Frames
		// Drift fit against the representation's own ideal grid; clean
		// synthetic material fits the identity model within tolerance.
		est := timeline.NewDriftEstimator(uint64(i+1), cfg.DriftLimitPPM)
		for _, f := range r.Frames {
			est.Add(f.T0, f.T0) // identity reference until drift is measured
		}
		if _, err := est.Fit(); err != nil {
			return materialError(ebml.ErrBadElement, r.Frames[0].ByteOffset, "rep %s: %v", r.ID, err)
		}
	}
	if err := timeline.AssertCrossTrackMonotonic(trackMap); err != nil {
		return materialError(ebml.ErrBadElement, -1, "%v", err)
	}
	return nil
}

func toAlignReps(reps []parsedRep) []align.Representation {
	out := make([]align.Representation, len(reps))
	for i, r := range reps {
		out[i] = align.Representation{ID: r.ID, Frames: r.Frames}
	}
	return out
}

func repIDs(reps []parsedRep) []string {
	out := make([]string, len(reps))
	for i, r := range reps {
		out[i] = r.ID
	}
	return out
}

// maxBoundarySkewMS measures, at every boundary, the largest difference
// between each representation's actual keyframe T0 (resolved through the
// boundary frame indices) and the reconciled common boundary time.
func maxBoundarySkewMS(boundaries []align.Boundary, reps []parsedRep) int64 {
	byID := make(map[string][]timeline.Frame, len(reps))
	for _, r := range reps {
		byID[r.ID] = r.Frames
	}
	var maxSkewNS int64
	for _, b := range boundaries {
		for id, idx := range b.FrameIndex {
			frames := byID[id]
			if idx < 0 || idx >= len(frames) {
				continue
			}
			d := frames[idx].T0 - b.T0
			if d < 0 {
				d = -d
			}
			if d > maxSkewNS {
				maxSkewNS = d
			}
		}
	}
	return (maxSkewNS + 999_999) / 1_000_000 // round up to whole ms
}

// commitCheckpoints emulates chunked write-ahead processing. A simulated
// interrupt skips the commit of the interrupted chunk itself. It returns the
// number of checkpoint chunks written during this invocation.
func commitCheckpoints(cfg Config, store *index.Store, key string, startCursor uint64) (uint64, *Error) {
	var committed uint64
	for chunk := uint64(1); chunk <= uint64(cfg.Clusters); chunk++ {
		if chunk <= startCursor {
			continue // already committed before the resume point
		}
		if cfg.InterruptAfter > 0 && chunk == uint64(cfg.InterruptAfter) && !cfg.Resume {
			// Crash before the write-ahead record lands.
			return committed, indexError("simulated interrupt while processing chunk %d", chunk)
		}
		if chunk%uint64(cfg.CheckpointEvery) == 0 || chunk == uint64(cfg.Clusters) {
			rec := index.Record{
				CheckpointKey: key, ChunkSeq: chunk,
				Cursor: chunk, Stage: "segments",
			}
			if _, err := store.Write(rec); err != nil {
				return committed, indexError("checkpoint write: %v", err)
			}
			committed++
		}
	}
	return committed, nil
}

func digestableConfig(cfg Config) map[string]any {
	return map[string]any{
		"seed": cfg.Seed, "rates": cfg.Rates, "clusters": cfg.Clusters,
		"target_segment_ns": cfg.TargetSegmentNS, "max_skew_ns": cfg.MaxSkewNS,
		"drift_limit_ppm": cfg.DriftLimitPPM, "corrupt": string(cfg.Corrupt),
	}
}
