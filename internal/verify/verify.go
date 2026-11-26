// Package verify orchestrates the terminal acceptance verification.
package verify

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"example.com/archiveverify/internal/cmaf"
	"example.com/archiveverify/internal/fixture"
	"example.com/archiveverify/internal/index"
	"example.com/archiveverify/internal/model"
	"example.com/archiveverify/internal/stream"
)

// Config controls one verification run.
type Config struct {
	Case         string
	Range        string
	WorkDir      string
	ManifestPath string
	Iterations   int
	MemoryBytes  int64
	Queue        int
	Resume       bool
	Repeat       int
}

// OffsetTrace is the audit view printed for timestamp repairs.
type OffsetTrace struct {
	Kind      string   `json:"kind"`
	Source    string   `json:"source"`
	TrackID   uint32   `json:"track_id"`
	BoxOffset int64    `json:"box_offset"`
	Original  int64    `json:"original"`
	Fixed     int64    `json:"fixed"`
	OrigRaw   int64    `json:"orig_raw"`
	FixedRaw  int64    `json:"fixed_raw"`
	OffsetUs  model.Us `json:"offset_us"`
	OrigUs    model.Us `json:"orig_us"`
	FixedUs   model.Us `json:"fixed_us"`
}

// Report is the stable terminal output object.
type Report struct {
	Case            string        `json:"case"`
	Range           string        `json:"range"`
	SyncMS          float64       `json:"sync_ms"`
	LossRecoveryPct float64       `json:"loss_recovery_pct"`
	P95MS           float64       `json:"p95_ms"`
	PeakMiB         float64       `json:"peak_mib"`
	MFHDGaps        int           `json:"mfhd_gaps"`
	TFDTRepairs     int           `json:"tfdt_repairs"`
	Quarantined     int           `json:"quarantined"`
	OffsetTrace     []OffsetTrace `json:"offset_trace"`
	DigestEqual     bool          `json:"digest_equal"`
	Digest          string        `json:"digest,omitempty"`
	Resumed         bool          `json:"resumed,omitempty"`
	VideoSamples    int           `json:"video_samples,omitempty"`
	AudioSamples    int           `json:"audio_samples,omitempty"`
	DecodePrefix    int           `json:"decode_prefix,omitempty"`
}

// Run generates data, builds/resumes the index, reads ranges and exercises pipe.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if cfg.Iterations <= 0 {
		cfg.Iterations = 100
	}
	if cfg.Repeat <= 0 {
		cfg.Repeat = 1
	}
	if cfg.MemoryBytes <= 0 {
		cfg.MemoryBytes = 8 << 20
	}
	if cfg.Queue <= 0 {
		cfg.Queue = 4
	}
	var manifestPath string
	var manifest *cmaf.Manifest
	if cfg.ManifestPath != "" {
		manifestPath = cfg.ManifestPath
		loaded, err := cmaf.LoadManifest(manifestPath)
		if err != nil {
			return nil, err
		}
		manifest = loaded
		if cfg.WorkDir == "" {
			cfg.WorkDir = filepath.Dir(manifestPath)
		}
	} else {
		if cfg.WorkDir == "" {
			cfg.WorkDir = filepath.Join(".verify-work", cfg.Case)
		}
		if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
			return nil, err
		}
		ws, err := fixture.Generate(cfg.WorkDir, cfg.Case)
		if err != nil {
			return nil, err
		}
		manifestPath = ws.ManifestPath
		manifest = ws.Manifest
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, err
	}

	rng, err := index.ParseRange(cfg.Range)
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(cfg.WorkDir, "index.json")
	checkpointPath := filepath.Join(cfg.WorkDir, "checkpoint.json")
	if cfg.Resume && cfg.Repeat > 1 {
		_ = os.Remove(indexPath)
		_ = os.Remove(checkpointPath)
	}

	var last *index.BuildResult
	var digests []string
	resumed := false
	for i := 0; i < cfg.Repeat; i++ {
		res, didResume, err := index.BuildOrResume(ctx, manifestPath, indexPath, checkpointPath, index.BuildOptions{
			RTPWindow: 16,
		})
		if err != nil {
			return nil, err
		}
		last = res
		resumed = resumed || didResume
		digests = append(digests, res.Index.Digest())
	}
	allEqual := true
	for _, d := range digests[1:] {
		if d != digests[0] {
			allEqual = false
		}
	}

	readRes, err := benchmarkReads(last.Index, rng, cfg.Iterations)
	if err != nil {
		return nil, err
	}
	pipeStats, err := runPipe(ctx, cfg, manifest, last.Index)
	if err != nil {
		return nil, err
	}

	var maxSync model.Us
	for _, a := range last.Anchors {
		if a.AbsErrorUs > maxSync {
			maxSync = a.AbsErrorUs
		}
	}
	traces := make([]OffsetTrace, 0, len(last.Index.Repairs))
	tfdtRepairs := 0
	for _, r := range last.Index.Repairs {
		if r.Kind == "tfdt_backward_monotonic_clamp" || r.Kind == "tfdt_v0_wraparound_extend" {
			tfdtRepairs++
		}
		traces = append(traces, OffsetTrace{
			Kind: r.Kind, Source: r.Source, TrackID: r.TrackID, BoxOffset: r.BoxOffset,
			Original: r.OrigRaw, Fixed: r.FixedRaw, OrigRaw: r.OrigRaw, FixedRaw: r.FixedRaw,
			OffsetUs: r.OffsetUs, OrigUs: r.OrigUs, FixedUs: r.FixedUs,
		})
	}

	return &Report{
		Case: cfg.Case, Range: cfg.Range,
		SyncMS:          math.Round(float64(maxSync)/1000.0*1000) / 1000,
		LossRecoveryPct: last.RTP.RecoveryPct,
		P95MS:           readRes.p95MS, PeakMiB: float64(pipeStats.PeakMemoryBytes) / float64(1<<20),
		MFHDGaps: len(last.Index.MFHDGaps), TFDTRepairs: tfdtRepairs,
		Quarantined: len(last.Index.Quarantine), OffsetTrace: traces,
		DigestEqual: allEqual, Digest: digests[len(digests)-1], Resumed: resumed,
		VideoSamples: len(readRes.result.Video), AudioSamples: len(readRes.result.Audio),
		DecodePrefix: len(readRes.result.DecodePrefix),
	}, nil
}

type benchmarkResult struct {
	p95MS  float64
	result *index.RangeResult
}

func benchmarkReads(idx *index.Index, rng index.Range, n int) (*benchmarkResult, error) {
	var last *index.RangeResult
	lat := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		rr, err := index.ReadRange(idx, rng)
		if err != nil {
			return nil, err
		}
		lat = append(lat, time.Since(start))
		last = rr
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p95idx := int(math.Ceil(0.95*float64(n))) - 1
	if p95idx < 0 {
		p95idx = 0
	}
	if p95idx >= len(lat) {
		p95idx = len(lat) - 1
	}
	return &benchmarkResult{p95MS: float64(lat[p95idx].Microseconds()) / 1000.0, result: last}, nil
}

func runPipe(ctx context.Context, cfg Config, m *cmaf.Manifest, idx *index.Index) (stream.Stats, error) {
	items := make([]stream.Item, 0, len(idx.Entries))
	for i, e := range idx.Entries {
		items = append(items, stream.Item{ID: i, Size: 4096, Payload: e})
	}
	slowDelay := time.Duration(0)
	slowItems := 0
	for _, ev := range m.Events {
		if ev.Kind == "slow_consumer" && ev.Slow != nil {
			slowDelay = time.Duration(ev.Slow.DelayMS) * time.Millisecond
			slowItems = ev.Slow.Items
		}
	}
	pipe := stream.NewPipe(stream.Options{Queue: cfg.Queue, MemoryBytes: cfg.MemoryBytes})
	return pipe.Run(ctx, items, func(ctx context.Context, item stream.Item) error {
		if slowItems > 0 && item.ID < slowItems {
			select {
			case <-time.After(slowDelay):
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		return nil
	})
}
