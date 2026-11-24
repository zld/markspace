package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"mediarun/internal/audio"
	"mediarun/internal/pipeline"
)

// ErrInterrupted marks a deliberate stop after N segments; all work up to that
// point is already checkpointed and the run can resume.
var ErrInterrupted = errors.New("run interrupted after requested segment count")

// Params is the canonical, cache-relevant processing configuration.
type Params struct {
	SampleRate int     `json:"sample_rate"`
	Channels   int     `json:"channels"`
	TargetLUFS float64 `json:"target_lufs"`
	SegmentMS  int64   `json:"segment_ms"`
}

// DefaultParams returns the mandated 48 kHz / stereo / -16 LUFS configuration.
func DefaultParams() Params {
	return Params{SampleRate: audio.TargetRate, Channels: audio.TargetChannels,
		TargetLUFS: audio.DefaultLoudness().IntegratedLUFS, SegmentMS: 1000}
}

func (p Params) canonical() string {
	raw, _ := json.Marshal(p)
	return string(raw)
}

// Config controls one processing invocation.
type Config struct {
	JobID          string
	InputDigest    string
	InputSize      int64
	PacketCount    int
	InputPath      string
	WorkDir        string // all writes stay inside this root
	CheckpointPath string
	CacheDir       string
	InterruptAfter int // <=0 means run to completion
}

// RunResult is the full outcome of one invocation.
type RunResult struct {
	OutputPath       string
	FinalDigest      string
	SegmentsTotal    int
	SegmentsNow      int // segments produced in this invocation
	SegmentsResumed  int // segments skipped because a checkpoint had them
	DuplicateSegment int // always zero: checkpoint commit is idempotent
	CacheHits        int
	CacheMisses      int
	MissReasons      map[string]int
	AVSyncMS         int64
	P95ProcessMS     int64
	Ingest           *IngestReport
	Provenance       *pipeline.Provenance
	Interrupted      bool
}

// Runner executes a configured processing run.
type Runner struct {
	normalizer *audio.Normalizer
	params     Params
}

// NewRunner builds a runner around a ready ffmpeg normalizer.
func NewRunner(n *audio.Normalizer) *Runner {
	return &Runner{normalizer: n, params: DefaultParams()}
}

// Run processes every segment with cache + checkpoint protection and writes
// the combined output and provenance document.
func (r *Runner) Run(ctx context.Context, rep *IngestReport, cfg Config) (*RunResult, error) {
	if len(rep.Segments) == 0 {
		return nil, errors.New("no reassemblable audio segments")
	}
	if err := ensureSafePath(cfg.WorkDir); err != nil {
		return nil, err
	}
	if err := ensureSafePath(cfg.CheckpointPath); err != nil {
		return nil, err
	}
	if err := ensureSafePath(cfg.CacheDir); err != nil {
		return nil, err
	}
	cache, err := pipeline.NewCache(cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	store, err := pipeline.NewCheckpointStore(cfg.CheckpointPath)
	if err != nil {
		return nil, err
	}
	cp, _, err := store.Load()
	if err != nil {
		return nil, err
	}
	prov := pipeline.NewProvenance(cfg.JobID)
	prov.Input = pipeline.InputProvenance{Path: cfg.InputPath, Digest: cfg.InputDigest,
		SizeBytes: cfg.InputSize, PacketCount: cfg.PacketCount}
	prov.Parameters = map[string]any{
		"sample_rate": r.params.SampleRate, "channels": r.params.Channels,
		"target_lufs": r.params.TargetLUFS, "segment_ms": r.params.SegmentMS,
	}
	prov.Tools = pipeline.ToolVersions{FFmpeg: r.normalizer.Version()}
	prov.Events = rep.Events

	// Replay already-committed segments into provenance so a resumed run still
	// documents the complete lineage, not only the segments processed now.
	committed := map[int]bool{}
	for _, rec := range cp.Segments {
		committed[rec.Index] = true
		prov.AddSegment(pipeline.SegmentProvenance{
			Index: rec.Index, SourceRange: rec.SourceRange, InputDigest: rec.InputDigest,
			OutputDigest: rec.OutputDigest, CacheHash: rec.CacheHash,
			FromCheckpoint: true, MissReason: "resumed_from_checkpoint", DurationMS: rec.DurationMS,
		})
	}
	prov.Parameters = map[string]any{
		"sample_rate": r.params.SampleRate, "channels": r.params.Channels,
		"target_lufs": r.params.TargetLUFS, "segment_ms": r.params.SegmentMS,
	}
	prov.Tools = pipeline.ToolVersions{FFmpeg: r.normalizer.Version()}
	prov.Events = rep.Events

	res := &RunResult{Ingest: rep, MissReasons: map[string]int{}, Provenance: prov}
	var latencies []int64
	processed := 0

	for _, seg := range rep.Segments {
		if cp.IsSegmentDone(seg.Index) {
			res.SegmentsResumed++
			continue
		}
		lat, sp, err := r.processSegment(ctx, cache, store, &cp, cfg, seg, prov)
		if err != nil {
			return res, err
		}
		latencies = append(latencies, lat)
		processed++
		res.SegmentsNow++
		if sp.FromCache {
			res.CacheHits++
		} else {
			res.CacheMisses++
			res.MissReasons[sp.MissReason]++
		}
		if cfg.InterruptAfter > 0 && processed >= cfg.InterruptAfter {
			res.Interrupted = true
			res.P95ProcessMS = percentile95(latencies)
			return res, ErrInterrupted
		}
	}

	// All segments are committed: combine them deterministically.
	finalPath, finalDigest, err := r.assemble(cfg, cp.Segments)
	if err != nil {
		return res, err
	}
	if err := store.MarkComplete(&cp, finalDigest); err != nil {
		return res, err
	}
	res.OutputPath = finalPath
	res.FinalDigest = finalDigest
	res.SegmentsTotal = cp.SegmentCount()
	res.AVSyncMS = maxAVSync(rep.Segments, r.params.SegmentMS)
	res.P95ProcessMS = percentile95(latencies)
	prov.Output = pipeline.OutputProvenance{Path: finalPath, Digest: finalDigest}
	provPath, _ := pipeline.SafeJoin(cfg.WorkDir, "provenance.json")
	if valid, err := prov.Validate(finalPath); err != nil || !valid {
		return res, err
	}
	if err := prov.Write(provPath); err != nil {
		return res, err
	}
	return res, nil
}

// processSegment handles one segment's full cache → normalize → checkpoint flow.
func (r *Runner) processSegment(ctx context.Context, cache *pipeline.Cache,
	store *pipeline.CheckpointStore, cp *pipeline.Checkpoint, cfg Config,
	seg Segment, prov *pipeline.Provenance) (int64, pipeline.SegmentProvenance, error) {
	key := pipeline.CacheKey{InputDigest: seg.InputDigest, Params: r.params.canonical(),
		Versions: pipeline.ToolVersions{FFmpeg: r.normalizer.Version()}}
	artifactName := filepath.Base(segmentArtifact(cfg, seg.Index))
	start := time.Now()
	entry, reason, err := cache.Get(key, seg.InputDigest, artifactName)
	if err != nil {
		return 0, pipeline.SegmentProvenance{}, err
	}
	var outBytes []byte
	fromCache := reason == pipeline.MissNone
	if fromCache {
		src, _ := pipeline.SafeJoin(cache.Root(), entry.ArtifactPath)
		outBytes, err = os.ReadFile(src)
		if err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
		// Restore the verified artifact into this run's segment directory so
		// the final assembly can read it like any freshly produced segment.
		outPath := segmentArtifact(cfg, seg.Index)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
		if err := os.WriteFile(outPath, outBytes, 0o600); err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
	} else {
		inPath, _ := pipeline.SafeJoin(cfg.WorkDir, "tmp", segmentInName(seg.Index))
		if err := os.MkdirAll(filepath.Dir(inPath), 0o755); err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
		if err := os.WriteFile(inPath, seg.Bytes, 0o600); err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
		outPath := segmentArtifact(cfg, seg.Index)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
		if _, err := r.normalizer.Normalize(ctx, inPath, outPath); err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
		outBytes, err = os.ReadFile(outPath)
		if err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
		if _, err := cache.Put(key, seg.InputDigest, artifactName, outBytes); err != nil {
			return 0, pipeline.SegmentProvenance{}, err
		}
	}
	lat := time.Since(start).Milliseconds()
	outDigest := pipeline.DigestSHA256(outBytes)
	record := pipeline.SegmentRecord{Index: seg.Index,
		SourceRange: [2]int64{seg.SourceStart, seg.SourceEnd},
		InputDigest: seg.InputDigest, OutputFile: artifactName,
		OutputDigest: outDigest, CacheHash: key.Hash(), DurationMS: r.params.SegmentMS}
	if err := store.CommitSegment(cp, record); err != nil {
		return 0, pipeline.SegmentProvenance{}, err
	}
	sp := pipeline.SegmentProvenance{Index: seg.Index, SourceRange: record.SourceRange,
		InputDigest: seg.InputDigest, OutputDigest: outDigest, CacheHash: key.Hash(),
		FromCache: fromCache, MissReason: string(reason), DurationMS: r.params.SegmentMS}
	prov.AddSegment(sp)
	return lat, sp, nil
}
