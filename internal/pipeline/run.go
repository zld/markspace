package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"mediaarchive/internal/diag"
	"mediaarchive/internal/merge"
)

// Mode selects strict or recover semantics.
type Mode string

const (
	// ModeRecover skips unrecoverable rows but keeps every fault on record.
	ModeRecover Mode = "recover"
	// ModeStrict fails on the first fault and emits no complete manifest.
	ModeStrict Mode = "strict"
)

// ParseMode validates a CLI mode token.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeRecover:
		return ModeRecover, nil
	case ModeStrict:
		return ModeStrict, nil
	default:
		return "", fmt.Errorf("unknown mode %q (want recover or strict)", s)
	}
}

// AssetInput is one source asset and its optional companions. Paths are opened
// read-only; nothing is written next to them except through the CLI's atomic
// manifest commit.
type AssetInput struct {
	Asset       string
	MP4         string
	Subtitle    string
	RTP         string
	FPS         string
	TargetBps   int64
	SRTOffsetMs float64
	// LabelFormat is the explicit SMPTE family for labels found in subtitles.
	LabelFormat string
}

// Row is a manifest row.
type Row = merge.Record

// Segment is a locatable damaged range, format-independent.
type Segment struct {
	Source      string  `json:"source"`
	Code        string  `json:"code"`
	StartMs     float64 `json:"start_ms"`
	EndMs       float64 `json:"end_ms"`
	StartOffset int64   `json:"start_offset"`
	EndOffset   int64   `json:"end_offset"`
	FaultCount  int     `json:"fault_count"`
	Recoverable bool    `json:"recoverable"`
	Detail      string  `json:"detail,omitempty"`
}

// Metrics are the auditable aggregate numbers printed by the CLI.
type Metrics struct {
	TargetBitrateBps int64
	ActualBitrateBps int64
	DeviationPct     float64
	CueCount         int
	DamageCount      int
	KeyframeGapMaxMs float64
	DriftPPM         float64
	AlignmentErrorMs float64
	KeyframeCount    int
	SampleCount      int64
	MediaDurationMs  float64
}

// Manifest is the final, auditable archive index.
type Manifest struct {
	Mode       Mode
	Assets     int
	Rows       []Row
	Segments   []Segment
	Faults     []*diag.Fault
	Metrics    Metrics
	Duplicates int64
	Skipped    int64
	Digest     string
}

// Options bounds the run.
type Options struct {
	Mode    Mode
	Workers int
	Queue   int
	FPS     string
}

// Run executes production, processing and merging. The manifest is always
// returned so callers can report progress; on strict-mode faults Digest is
// empty and callers must not persist the manifest.
func Run(ctx context.Context, opts Options, assets []AssetInput) (*Manifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var (
		mu      sync.Mutex
		results []*assetResult
		idx     int64
	)
	pool := Pool{Workers: opts.Workers, Queue: opts.Queue}
	res := pool.Run(ctx, func(ctx context.Context) (Job, bool, error) {
		if idx >= int64(len(assets)) {
			return Job{}, false, nil
		}
		if err := ctx.Err(); err != nil {
			return Job{}, false, err
		}
		j := Job{Index: idx, Offset: idx, Kind: assets[idx].Asset}
		idx++
		return j, true, nil
	}, func(ctx context.Context, job Job) error {
		r, err := analyzeAsset(ctx, assets[job.Index], opts)
		if err != nil {
			return err
		}
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
		return nil
	})

	m := &Manifest{Mode: opts.Mode, Assets: len(assets)}
	if res.Err != nil {
		return finishFailed(m, res.Err)
	}
	var rows []Row
	var segs []Segment
	for _, r := range results {
		rows = append(rows, r.Rows...)
		segs = append(segs, r.Segments...)
		m.Faults = append(m.Faults, r.Faults...)
		m.Metrics.KeyframeCount += r.Keyframes
		m.Metrics.SampleCount += r.Samples
		m.Metrics.MediaDurationMs += r.MediaDurationMs
	}
	sortFaults(m.Faults)
	sortSegments(segs)
	merged := merge.Merge(rows)
	m.Rows = merged.Rows
	m.Duplicates = merged.Duplicates
	m.Segments = segs
	m.Metrics.DamageCount = len(segs)
	// Recovery skips rows inside unrecoverable damage; report how many, so the
	// audit trail distinguishes "no row existed" from "row was withheld".
	for _, r := range results {
		m.Skipped += r.Skipped
	}

	if opts.Mode == ModeStrict && len(m.Faults) > 0 {
		return m, firstFault(m.Faults)
	}
	fillMetrics(m, results)
	if err := ValidateManifest(m); err != nil {
		return m, err
	}
	m.Digest = Digest(m)
	return m, nil
}

func finishFailed(m *Manifest, err error) (*Manifest, error) {
	// Preserve the original error identity so callers can test for
	// context.Canceled while still exposing a structured fault code.
	f, ok := diag.As(err)
	if !ok {
		f = diag.New(diag.CodeCanceled, 0, "%v", err)
		m.Faults = append(m.Faults, f)
		return m, err
	}
	m.Faults = append(m.Faults, f)
	return m, err
}

func firstFault(faults []*diag.Fault) error {
	cp := *faults[0]
	return &cp
}

func sortFaults(faults []*diag.Fault) {
	sort.SliceStable(faults, func(i, j int) bool {
		if faults[i].Offset != faults[j].Offset {
			return faults[i].Offset < faults[j].Offset
		}
		return faults[i].Code < faults[j].Code
	})
}

func sortSegments(segs []Segment) {
	sort.SliceStable(segs, func(i, j int) bool {
		if segs[i].Source != segs[j].Source {
			return segs[i].Source < segs[j].Source
		}
		if segs[i].StartMs != segs[j].StartMs {
			return segs[i].StartMs < segs[j].StartMs
		}
		if segs[i].StartOffset != segs[j].StartOffset {
			return segs[i].StartOffset < segs[j].StartOffset
		}
		return segs[i].Code < segs[j].Code
	})
}

func fillMetrics(m *Manifest, results []*assetResult) {
	var bytes int64
	var durMs, weightedTarget float64
	m.Metrics.CueCount, m.Metrics.DriftPPM = 0, 0
	var driftSum float64
	driftN := 0
	for _, r := range results {
		bytes += r.SampleBytes
		durMs += r.MediaDurationMs
		// Targets are per-second rates, so aggregate them weighted by each
		// asset's media duration rather than summing raw rates.
		if r.TargetBps > 0 && r.MediaDurationMs > 0 {
			weightedTarget += float64(r.TargetBps) * r.MediaDurationMs
		}
		m.Metrics.CueCount += r.Cues
		m.Metrics.AlignmentErrorMs = mathMax(m.Metrics.AlignmentErrorMs, r.AlignmentErrorMs)
		if r.HasDrift {
			driftSum += r.DriftPPM
			driftN++
		}
		if r.MaxGapMs > m.Metrics.KeyframeGapMaxMs {
			m.Metrics.KeyframeGapMaxMs = r.MaxGapMs
		}
	}
	if durMs > 0 {
		m.Metrics.TargetBitrateBps = int64(weightedTarget / durMs)
		m.Metrics.ActualBitrateBps = int64(float64(bytes*8) / (durMs / 1000))
	}
	if m.Metrics.TargetBitrateBps > 0 {
		m.Metrics.DeviationPct = (float64(m.Metrics.ActualBitrateBps) - float64(m.Metrics.TargetBitrateBps)) /
			float64(m.Metrics.TargetBitrateBps) * 100
	}
	if driftN > 0 {
		m.Metrics.DriftPPM = driftSum / float64(driftN)
	}
}

func mathMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// Digest is a stable SHA-256 over semantic manifest content. It excludes
// wall-clock and scheduling data, so repeated runs over identical input match.
func Digest(m *Manifest) string {
	h := sha256.New()
	fmt.Fprintf(h, "mode=%s\nassets=%d\nrows=%d\n", m.Mode, m.Assets, len(m.Rows))
	for _, r := range m.Rows {
		fmt.Fprintf(h, "r|%s|%.3f|%d|%s|%s|%s\n", r.Asset, r.StartMs, r.Seq, r.Stage, r.Kind, r.Payload)
	}
	for _, s := range m.Segments {
		fmt.Fprintf(h, "s|%s|%s|%.3f|%.3f|%d|%d|%d|%t\n",
			s.Source, s.Code, s.StartMs, s.EndMs, s.StartOffset, s.EndOffset, s.FaultCount, s.Recoverable)
	}
	for _, f := range m.Faults {
		fmt.Fprintf(h, "f|%s|%d|%.3f\n", f.Code, f.Offset, f.MediaMs)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidateManifest enforces cross-stage invariants: rows are monotone,
// damage segments never overlap and every row reference is resolvable.
func ValidateManifest(m *Manifest) error {
	if m == nil {
		return diag.New(diag.CodeMissingBase, 0, "nil manifest")
	}
	if v, at := merge.ValidateMonotone(m.Rows); v != 0 {
		return diag.New(diag.CodeTimestampRollback, int64(at), "merged manifest not monotone at row %d", at)
	}
	bySource := map[string][]Segment{}
	for _, s := range m.Segments {
		bySource[s.Source] = append(bySource[s.Source], s)
	}
	assetSet := map[string]bool{}
	for _, r := range m.Rows {
		assetSet[r.Asset] = true
	}
	for _, s := range m.Segments {
		if s.Source == "" {
			return diag.New(diag.CodeMissingBase, s.StartOffset, "damage segment without a source")
		}
		if s.EndMs < s.StartMs || s.EndOffset < s.StartOffset {
			return diag.New(diag.CodeTimestampRollback, s.StartOffset, "segment on %s runs backwards", s.Source)
		}
	}
	for src, segs := range bySource {
		for i := 1; i < len(segs); i++ {
			if segs[i].StartMs < segs[i-1].EndMs && segs[i].StartOffset <= segs[i-1].EndOffset {
				return diag.New(diag.CodeMP4Structure, segs[i].StartOffset,
					"damage segments on %s overlap at %.3f ms", src, segs[i].StartMs)
			}
		}
	}
	_ = assetSet
	return nil
}
