// Package testkit also hosts the end-to-end pipeline harness used by both
// the acceptance demo and the cross-stage tests. It wires scan -> index ->
// repair -> stream -> metrics exactly as production does, on generated
// fixtures.
package testkit

import (
	"context"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/index"
	"mediacore/internal/metrics"
	"mediacore/internal/repair"
	"mediacore/internal/scan"
	"mediacore/internal/stream"
)

// PipelineConfig are the demo/test knobs mapped onto subsystem configs.
type PipelineConfig struct {
	TargetBPS       uint64
	MaxBatchSamples int
	MaxBatchBytes   int64
	InFlightBytes   int64
	SlowEvery       int
}

// PipelineResult carries every value the report and tests assert on.
type PipelineResult struct {
	Report       metrics.Report
	CacheState   string
	RepairDigest string
	OutputDigest string
	Records      uint32
	RepairStats  repair.Stats
	Seek         scan.SeekResult
}

// fileConsumer writes payloads to an output file (never the source) and
// returns logical wire bytes for metrics.
type fileConsumer struct {
	out       *os.File
	slowEvery int
	batchN    int
	sleep     func()
}

func (c *fileConsumer) Consume(ctx context.Context, b stream.Batch) (int64, error) {
	if c.slowEvery > 0 && c.batchN > 0 && c.batchN%c.slowEvery == 0 {
		// In tests the sleep hook is replaced; production injects a real one.
		if c.sleep != nil {
			c.sleep()
		}
	}
	var logical int64
	for _, r := range b.Records {
		if _, err := c.out.Write(r.Payload); err != nil {
			return 0, clock.ErrRecoverablef("pipeline.consume", "write: %v", err)
		}
		logical += int64(r.WireSize)
	}
	c.batchN++
	return logical, nil
}

// RunPipeline executes the full bounded pipeline on one fixture set and
// returns the evidence bundle. indexCachePath points at a pre-seeded (or
// absent) cache file; its state is reported as hit/rebuilt.
func RunPipeline(ctx context.Context, fs FixtureSet, pcfg PipelineConfig,
	indexCachePath string, sleepHook func()) (*PipelineResult, error) {
	manifest, digest, err := scan.LoadManifest(fs.MP4, fs.Manifest)
	if err != nil {
		return nil, err
	}
	return runWithManifest(ctx, fs, pcfg, indexCachePath, sleepHook, manifest, digest)
}

// RunPipelineTrustedDigest runs the pipeline using a caller-supplied input
// digest instead of re-hashing the MP4. It exists for failure-injection tests
// that corrupt bytes after a valid index exists, so the cache key stays
// stable and the execution-stage bounds check is what fails.
func RunPipelineTrustedDigest(ctx context.Context, fs FixtureSet, pcfg PipelineConfig,
	trustedDigest string, sleepHook func()) (*PipelineResult, error) {
	manifest, err := scan.LoadManifestWithDigest(fs.Manifest, trustedDigest)
	if err != nil {
		return nil, err
	}
	return runWithManifest(ctx, fs, pcfg, "", sleepHook, manifest, trustedDigest)
}

func runWithManifest(ctx context.Context, fs FixtureSet, pcfg PipelineConfig,
	indexCachePath string, sleepHook func(), manifest *scan.Manifest, digest string) (*PipelineResult, error) {
	scanCfg := scan.DefaultConfig()
	mp4f, tracks, err := scan.OpenFile(fs.MP4, manifest, scanCfg)
	if err != nil {
		return nil, err
	}
	defer mp4f.Close()
	track := tracks[0]

	// Index: verify cache or build. The cache file path is canonical for the
	// (input digest, track) pair; indexCachePath must equal it.
	cachePath := index.CachePath(fs.Index, digest, manifest.TrackID)
	if indexCachePath != "" && indexCachePath != cachePath {
		return nil, clock.ErrInvalidArgument("pipeline: cache path %s does not match canonical %s",
			indexCachePath, cachePath)
	}
	cacheState := "hit"
	hdr, err := index.LoadOrVerify(cachePath, digest, manifest.TrackID, manifest.SampleCount)
	if err != nil {
		if !index.IsCacheMiss(err) {
			return nil, err
		}
		cacheState = "rebuilt"
		b, berr := index.NewBuilder(fs.Index, digest, manifest.TrackID, manifest.Timescale, manifest.SampleCount)
		if berr != nil {
			return nil, berr
		}
		if _, _, berr = b.Build(track, scanCfg); berr != nil {
			return nil, berr
		}
		hdr, err = index.LoadOrVerify(cachePath, digest, manifest.TrackID, manifest.SampleCount)
		if err != nil {
			return nil, berr
		}
	}
	idxFile, err := os.Open(cachePath)
	if err != nil {
		return nil, clock.ErrRecoverablef("pipeline.index", "open cache: %v", err)
	}
	defer idxFile.Close()

	// Repair over the RTP sidecar (optional: absent sidecar is allowed).
	var (
		repairDigest string
		repairStats  repair.Stats
	)
	if _, statErr := os.Stat(fs.RTP); statErr == nil {
		rr, rerr := repair.RunFile(ctx, fs.RTP, repair.DefaultConfig())
		if rerr != nil {
			return nil, rerr
		}
		repairDigest, repairStats = rr.Digest, rr.Stats
	}

	// Keyframe fallback evidence at a mid-stream target offset from any
	// sync position so the quantified fallback is non-zero.
	syncNums := track.SyncNumbers()
	seekTarget := manifest.SampleCount/2 + 1
	seekRes, err := scan.SeekDecodable(manifest.TrackID, seekTarget, manifest.SampleCount,
		syncNums, func(idx uint32) clock.Nanos {
			rec, rerr := index.ReadRecord(idxFile, hdr, idx)
			if rerr != nil {
				return 0
			}
			return clock.Nanos(rec.DTSNanos)
		})
	if err != nil {
		return nil, err
	}

	// Stream execution with byte-accounted backpressure.
	// Open for append: stream.Run has already aligned the file to the
	// checkpoint, so resumed batches continue exactly where the prior run
	// stopped instead of overwriting or duplicating earlier samples.
	out, err := os.OpenFile(fs.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, clock.ErrRecoverablef("pipeline.output", "open: %v", err)
	}
	consumer := &fileConsumer{out: out, slowEvery: pcfg.SlowEvery, sleep: sleepHook}
	scfg := stream.Config{
		MaxBatchSamples:  pcfg.MaxBatchSamples,
		MaxBatchBytes:    pcfg.MaxBatchBytes,
		MaxInFlightBytes: pcfg.InFlightBytes,
		SlowEvery:        pcfg.SlowEvery,
	}
	counters, cp, err := stream.Run(ctx, scfg,
		stream.Sources{MP4: mp4f, IndexFile: idxFile, Header: hdr},
		fs.Checkpoint, fs.Output, fs.MP4, consumer)
	out.Close()
	if err != nil {
		return nil, err
	}

	// Metrics over the same integer timeline.
	span := clock.Nanos(manifest.EndNanos - manifest.StartNanos)
	actual, err := metrics.Bitrate(counters.LogicalBytes, span)
	if err != nil {
		return nil, err
	}
	dev, err := metrics.DeviationHundredths(pcfg.TargetBPS, actual)
	if err != nil {
		return nil, err
	}
	sizes := manifest.WireSizes
	q, err := metrics.ComputeQuality(sizes)
	if err != nil {
		return nil, err
	}
	observedEnd := manifest.StartNanos + int64(span)
	report := metrics.Report{
		TargetBPS:          pcfg.TargetBPS,
		ActualBPS:          actual,
		BitrateDeviationPC: dev,
		QualitySamples:     q.SampleCount,
		DroppedSamples:     uint64(repairStats.GapPackets),
		AlignmentErrorMS:   metrics.AlignmentErrorMS(clock.Nanos(observedEnd), clock.Nanos(manifest.EndNanos)),
		KeyframeFallbackMS: nanosToMS(seekRes.FallbackNanos),
		PeakBufferBytes:    counters.PeakInFlight,
		CacheState:         cacheState,
		RepairDigest:       repairDigest,
		OutputDigest:       cp.OutputDigest,
	}
	return &PipelineResult{
		Report: report, CacheState: cacheState, RepairDigest: repairDigest,
		OutputDigest: cp.OutputDigest, Records: hdr.SampleCount,
		RepairStats: repairStats, Seek: seekRes,
	}, nil
}

func nanosToMS(n clock.Nanos) int64 {
	v := int64(n)
	ms := v / 1_000_000
	if rem := v % 1_000_000; rem*2 >= 1_000_000 {
		ms++
	}
	return ms
}

// CacheFileFor returns the conventional cache file path for a digest pair.
func CacheFileFor(dir, digestHex string, trackID uint32) string {
	return index.CachePath(dir, digestHex, trackID)
}

var _ = filepath.Join
