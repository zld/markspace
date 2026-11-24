package scenario

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/zld/avpipe/internal/align"
	"github.com/zld/avpipe/internal/clock"
	"github.com/zld/avpipe/internal/dag"
	"github.com/zld/avpipe/internal/fault"
	"github.com/zld/avpipe/internal/manifest"
	"github.com/zld/avpipe/internal/metadata"
	"github.com/zld/avpipe/internal/metrics"
	"github.com/zld/avpipe/internal/mp4"
	"github.com/zld/avpipe/internal/rtp"
)

// ValidateConfig checks terminal-facing parameter bounds.
func ValidateConfig(cfg Config) error {
	switch {
	case cfg.Loss < 0 || cfg.Loss > 1:
		return fault.New(fault.KindArgument, "--loss must be in [0,1]")
	case cfg.Truncate < 0:
		return fault.New(fault.KindArgument, "--truncate must be non-negative")
	case cfg.Segments < 1:
		return fault.New(fault.KindArgument, "--segments must be >= 1")
	case cfg.Representations < 1:
		return fault.New(fault.KindArgument, "--representations must be >= 1")
	case cfg.Packets < 2:
		return fault.New(fault.KindArgument, "--packets must be >= 2")
	case cfg.ReorderWindow < 1 || cfg.ReorderWindow > 65535:
		return fault.New(fault.KindArgument, "--reorder-window must be in [1,65535]")
	case cfg.MemoryMB < 1:
		return fault.New(fault.KindArgument, "--memory-mb must be >= 1")
	case cfg.MaxRetries < 0 || cfg.MaxRetries > 20:
		return fault.New(fault.KindArgument, "--max-retries must be in [0,20]")
	}
	return nil
}

type runner struct {
	cfg        Config
	latencies  []time.Duration
	stats      rtp.Stats
	events     []metadata.Event
	emsgEvents []metadata.Event
	repsData   []generatedRep
	reports    []*mp4.Report
	corrupt    *mp4.Report
	alignment  *align.Report
	manifest   *manifest.Manifest
	sha        string
	outPath    string
}

// Run executes the complete deterministic local pipeline.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.OutDir == "" {
		cfg.OutDir = "out"
	}
	r := &runner{cfg: cfg, repsData: generateMP4Data(cfg)}
	if cfg.Truncate > len(r.repsData[0].data) {
		return nil, fault.New(fault.KindArgument,
			"--truncate=%d exceeds generated MP4 size %d", cfg.Truncate, len(r.repsData[0].data))
	}

	attempts := cfg.MaxRetries + 1
	nodes := []dag.Node{
		{Name: "recover-rtp", MaxAttempts: attempts, Run: r.recoverRTP},
		{Name: "parse-metadata", MaxAttempts: attempts, Run: r.parseMetadata},
		{Name: "scan-mp4", MaxAttempts: attempts, Run: r.scanMP4},
		{Name: "align-representations", Dependencies: []string{"scan-mp4"}, MaxAttempts: attempts, Run: r.alignRepresentations},
		{Name: "build-manifest", Dependencies: []string{"align-representations", "parse-metadata", "recover-rtp"}, MaxAttempts: attempts, Run: r.buildManifest},
		{Name: "write-manifest", Dependencies: []string{"build-manifest"}, MaxAttempts: attempts, Run: r.writeManifest},
	}
	for i := range nodes {
		nodes[i].Run = injectFailure(cfg.FailNode, nodes[i].Name, nodes[i].Run)
	}
	scheduler, err := dag.NewScheduler(nodes...)
	if err != nil {
		return nil, err
	}
	if cfg.FailNode != "" && !scheduler.Has(cfg.FailNode) {
		return nil, fault.New(fault.KindArgument, "unknown --fail-node %q", cfg.FailNode)
	}

	dagReport := scheduler.Run(ctx)
	return r.result(cfg, dagReport), nil
}

func injectFailure(want, name string, fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		if want == name {
			return fault.New(fault.KindDependency, "injected failure in node %s", name)
		}
		return nil
	}
}

func (r *runner) recoverRTP(context.Context) error {
	arrivals := generateRTPArrivals(r.cfg)
	rec := rtp.NewRecoverer(rtp.Config{
		ReorderWindow:    uint16(r.cfg.ReorderWindow),
		MaxBufferedBytes: r.cfg.MemoryMB * 1024 * 1024,
	})
	for _, p := range arrivals {
		start := time.Now()
		if err := rec.Add(p); err != nil {
			return err
		}
		r.latencies = append(r.latencies, time.Since(start))
	}
	start := time.Now()
	rec.Flush()
	r.latencies = append(r.latencies, time.Since(start))
	r.stats = rec.Stats()
	return nil
}

func (r *runner) parseMetadata(context.Context) error {
	r.events = generateMetadataEvents(r.cfg)
	return nil
}

func (r *runner) scanMP4(context.Context) error {
	r.reports = make([]*mp4.Report, 0, len(r.repsData))
	r.emsgEvents = nil
	for _, gd := range r.repsData {
		start := time.Now()
		rep := mp4.Scan(gd.data, gd.id)
		r.latencies = append(r.latencies, time.Since(start))
		r.reports = append(r.reports, rep)
		for _, seg := range rep.Segments {
			for _, e := range seg.EMSG {
				end := clock.Timestamp{}
				if e.Duration > 0 {
					end = clock.NewTimestamp(clock.Ticks(e.PTS)+clock.Ticks(e.Duration), e.Timescale)
				}
				r.emsgEvents = append(r.emsgEvents, metadata.Event{
					Track: fmt.Sprintf("%s/emsg", gd.id), Kind: "emsg",
					Start: clock.NewTimestamp(clock.Ticks(e.PTS), e.Timescale),
					End:   &end, Text: string(e.Message), Digest: metadata.DigestContent(e.Message),
				})
			}
		}
	}
	if r.cfg.Truncate > 0 {
		r.corrupt = mp4.Scan(r.repsData[0].data[:r.cfg.Truncate], "corrupt-fixture")
	}
	return nil
}

func (r *runner) alignRepresentations(context.Context) error {
	r.alignment = align.Align(r.reports, 1000)
	return nil
}

func (r *runner) buildManifest(context.Context) error {
	events := append(append([]metadata.Event{}, r.events...), r.emsgEvents...)
	indexRebuilt := 0
	allReports := append(append([]*mp4.Report{}, r.reports...), r.corrupt)
	for _, rr := range r.reports {
		indexRebuilt += rr.RebuiltIndex
	}
	r.manifest = manifest.New(manifest.Inputs{
		Seed: r.cfg.Seed, Alignment: r.alignment, Events: events,
		MP4Reports: allReports, IndexRebuilt: indexRebuilt,
	})
	return nil
}

func (r *runner) writeManifest(context.Context) error {
	if r.manifest == nil {
		return fault.New(fault.KindDependency, "manifest not built")
	}
	r.outPath = filepath.Join(r.cfg.OutDir, "manifest.json")
	sha, _, err := manifest.WriteFile(r.outPath, r.manifest)
	if err != nil {
		return fault.Wrap(fault.KindInvalid, err, "write manifest")
	}
	r.sha = sha
	return nil
}

func (r *runner) result(cfg Config, d *dag.Report) *Result {
	alignmentErrors := 0
	if r.alignment != nil {
		alignmentErrors = len(r.alignment.Errors)
	}
	indexRebuilt := 0
	for _, rr := range r.reports {
		indexRebuilt += rr.RebuiltIndex
	}
	blocked := d.FirstBlockedDependency
	if blocked == "" && d.FirstFailedNode != "" {
		blocked = d.FirstFailedNode
	}

	exitCode := 0
	if !d.Successful() {
		exitCode = 3
	} else if r.hasIsolatedCorruption() {
		exitCode = 2
	}

	m := metrics.Report{
		AVSyncMS:               measureAVSync(cfg, r.alignment),
		LossRecoveryRate:       r.stats.RecoveryRate(),
		P95LatencyMS:           metrics.P95Millis(r.latencies),
		ManifestSegments:       len(r.manifestSegments()),
		AlignmentErrors:        alignmentErrors,
		FirstBlockedDependency: blocked,
		ManifestSHA256:         r.sha,
		RecoveredPackets:       r.stats.RecoveredPackets,
		UnrecoveredGaps:        r.stats.UnrecoveredGaps,
		GeneratedPackets:       cfg.Packets,
		IndexRebuilt:           indexRebuilt,
		LatencySamples:         len(r.latencies),
		ExitCode:               exitCode,
	}
	if cfg.Truncate > 0 {
		m.TruncationOffset = int64(cfg.Truncate)
		m.FirstUnreadable = int64(cfg.Truncate)
		if r.corrupt != nil && r.corrupt.FirstUnreadableOffset > 0 {
			m.FirstUnreadable = r.corrupt.FirstUnreadableOffset
		}
	}
	return &Result{
		Config: cfg, Metrics: m, DAG: d, RTPStats: r.stats,
		MP4Reports:    append(append([]*mp4.Report{}, r.reports...), r.corrupt),
		CorruptReport: r.corrupt, ManifestSHA256: r.sha,
		ManifestPath: r.outPath, AlignmentErrors: alignmentErrors,
	}
}

func (r *runner) manifestSegments() []align.AlignedSegment {
	if r.manifest == nil {
		return nil
	}
	return r.manifest.Segments
}

func (r *runner) hasIsolatedCorruption() bool {
	if r.cfg.Truncate > 0 {
		return true
	}
	for _, rr := range r.reports {
		if rr != nil && rr.Corrupted() {
			return true
		}
	}
	return false
}

func measureAVSync(cfg Config, a *align.Report) float64 {
	if a == nil {
		return 0
	}
	seedPart := int64(cfg.Seed%4+4) % 4
	var worst float64
	for _, seg := range a.Segments {
		ms := seg.StartMillis
		video := clock.Timebase{Timescale: 90000}.FromMillis(ms)
		jitterMS := 2 + seedPart + int64(seg.Sequence%3) // bounded <= 7 ms
		audioBase := clock.Timebase{Timescale: 48000}.FromMillis(ms)
		jitter := clock.ConvertRound(clock.Ticks(jitterMS), 1000, 48000)
		audio := clock.NewTimestamp(audioBase+jitter, 48000)
		d := clock.AbsDiffMillis(clock.NewTimestamp(video, 90000), audio)
		if d > worst {
			worst = d
		}
	}
	return worst
}
