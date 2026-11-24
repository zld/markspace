package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"mediaroute/internal/fixture"
	"mediaroute/internal/model"
	"mediaroute/internal/pipeline"
	"mediaroute/internal/report"
	"mediaroute/internal/timeline"
)

// demoCase is one printed JSON record.
type demoCase struct {
	Case                string               `json:"case"`
	OK                  bool                 `json:"ok"`
	TargetBPS           int64                `json:"target_bps"`
	ActualBPS           int64                `json:"actual_bps"`
	BitrateDeltaPct     float64              `json:"bitrate_delta_pct"`
	ValidSamplePct      float64              `json:"valid_sample_pct"`
	DroppedFragments    int                  `json:"dropped_fragments"`
	ManifestAlignmentUS model.Micros         `json:"manifest_alignment_us"`
	IndexEntries        int                  `json:"index_entries"`
	RepairedTfdt        int                  `json:"repaired_tfdt"`
	MFHDGaps            int                  `json:"mfhd_gaps"`
	TruncatedBoxes      int                  `json:"truncated_boxes"`
	RecoveredFragments  int                  `json:"recovered_fragments"`
	SHA256              string               `json:"sha256"`
	ManifestSHA256      string               `json:"manifest_sha256,omitempty"`
	OutputPath          string               `json:"output_path,omitempty"`
	ManifestPath        string               `json:"manifest_path,omitempty"`
	IndexPath           string               `json:"index_path,omitempty"`
	RTPWraps            int                  `json:"rtp_wraps,omitempty"`
	DamagedIndex        bool                 `json:"damaged_index"`
	CorruptIndexHints   int                  `json:"corrupt_index_hints"`
	Error               string               `json:"error,omitempty"`
	ErrorCategory       string               `json:"error_category,omitempty"`
	Repairs             []model.RepairRecord `json:"repairs,omitempty"`
	Monotonic           bool                 `json:"monotonic"`
}

func runDemo(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "fixture seed; identical seeds yield identical output")
	queue := fs.Int("queue", 2, "bounded pipeline queue capacity")
	target := fs.Int64("target-bps", 2_000_000, "target total bitrate in bits per second")
	outDir := fs.String("out-dir", "./tmp", "directory for delivered media and manifests")
	if err := fs.Parse(args); err != nil {
		return model.WrapTaskError(model.CategoryInvalidConfig, "cli", "parse demo flags", err)
	}
	if *queue <= 0 {
		return model.NewTaskError(model.CategoryInvalidConfig, "cli", "queue capacity must be positive")
	}
	if *target <= 0 {
		return model.NewTaskError(model.CategoryInvalidConfig, "cli", "target bitrate must be positive")
	}

	cfg := pipeline.DefaultConfig()
	cfg.QueueCapacity = *queue
	cfg.MaxBoxBytes = 8 << 20
	proc, err := pipeline.NewProcessor(cfg)
	if err != nil {
		return err
	}

	cases := []fixture.Damage{
		fixture.DamageNone,
		fixture.DamageSequenceGap,
		fixture.DamageTfdtRegression,
		fixture.DamageTruncatedBox,
		fixture.DamageCorruptIndex,
	}

	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	failures := 0
	for _, damage := range cases {
		rec := runOne(proc, *seed, *target, *outDir, damage)
		if !rec.OK {
			failures++
		}
		if err := enc.Encode(rec); err != nil {
			return model.WrapTaskError(model.CategoryIO, "cli", "encode demo record", err)
		}
	}
	if failures > 0 {
		return model.NewTaskError(model.CategoryDownstream, "cli",
			fmt.Sprintf("%d demo case(s) failed", failures))
	}
	fmt.Fprintf(stderr, "demo: wrote artifacts to %s\n", *outDir)
	return nil
}

func runOne(proc *pipeline.Processor, seed, target int64, outDir string, damage fixture.Damage) demoCase {
	rec := demoCase{Case: damage.String(), TargetBPS: target}
	params := fixture.DefaultParams(seed)
	params.TargetBPS = target
	gen, err := fixture.Generate(params, damage)
	if err != nil {
		rec.Error = err.Error()
		rec.ErrorCategory = string(model.CategoryInvalidBox)
		return rec
	}
	name := "demo-" + damage.String()
	mediaName := name + ".mp4"
	manifestName := name + "-manifest.json"
	indexName := name + "-index.json"
	if damage == fixture.DamageNone {
		// The acceptance flow verifies the canonical pair below, so the clean
		// case is also written under those stable names.
		mediaName = "demo-output.mp4"
		manifestName = "demo-manifest.json"
		indexName = "demo-index.json"
	}
	mediaPath, err := safeOutputPath(outDir, mediaName)
	if err != nil {
		rec.Error = err.Error()
		rec.ErrorCategory = string(model.CategoryPathTraversal)
		return rec
	}
	manifestPath, err := safeOutputPath(outDir, manifestName)
	if err != nil {
		rec.Error = err.Error()
		rec.ErrorCategory = string(model.CategoryPathTraversal)
		return rec
	}
	indexPath, err := safeOutputPath(outDir, indexName)
	if err != nil {
		rec.Error = err.Error()
		rec.ErrorCategory = string(model.CategoryPathTraversal)
		return rec
	}

	ctx := context.Background()
	outcome, err := proc.Process(ctx, gen.Bytes, nil)
	if err != nil {
		rec.Error = err.Error()
		if te, ok := model.AsTaskError(err); ok {
			rec.ErrorCategory = string(te.Category)
		}
		return rec
	}

	// A clean run must also produce a valid RTP report when the fixture
	// carries RTP timestamps. The demo synthesises them from the video track
	// clock so the modular unwrap path is exercised.
	rtpReport := demoRTP(outcome.Source.Tracks, outcome.Index)

	manifest, err := report.Build(report.BuildInputs{
		Name:         name,
		Seed:         seed,
		OutputPath:   mediaPath,
		IndexPath:    indexPath,
		OutputSize:   uint64(len(outcome.Output)),
		SourceBytes:  uint64(len(gen.Bytes)),
		OutputSHA256: outcome.SHA256,
		TargetBPS:    target,
		Source:       outcome.Source,
		Repairs:      outcome.Repairs,
		Index:        outcome.Index,
		Counts: report.Counts{
			MFHDGaps:     outcome.Counts.MFHDGaps,
			Duplicates:   outcome.Counts.Duplicates,
			RepairedTfdt: outcome.Counts.RepairedTfdt,
			Truncated:    outcome.Counts.Truncated,
			Dropped:      outcome.Counts.Dropped,
			Recovered:    outcome.Counts.Recovered,
		},
		RTP: rtpReport,
	})
	if err != nil {
		rec.Error = err.Error()
		if te, ok := model.AsTaskError(err); ok {
			rec.ErrorCategory = string(te.Category)
		}
		return rec
	}

	indexDoc, err := report.BuildIndexDocument(name, outcome.Index)
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	indexJSON, err := report.MarshalIndex(indexDoc)
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	canonical, err := report.MarshalCanonical(manifest)
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	digest, err := report.Digest(manifest)
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	if err := writeFileAtomic(mediaPath, outcome.Output); err != nil {
		rec.Error = err.Error()
		return rec
	}
	if err := writeFileAtomic(manifestPath, canonical); err != nil {
		rec.Error = err.Error()
		return rec
	}
	if err := writeFileAtomic(indexPath, indexJSON); err != nil {
		rec.Error = err.Error()
		return rec
	}

	rec.OK = true
	rec.ActualBPS = manifest.ActualBPS
	rec.BitrateDeltaPct = manifest.BitrateDeltaPct
	rec.ValidSamplePct = manifest.ValidSamplePct
	rec.DroppedFragments = manifest.DroppedFragments
	rec.ManifestAlignmentUS = manifest.ManifestAlignmentUS
	rec.IndexEntries = manifest.IndexEntries
	rec.RepairedTfdt = manifest.RepairedTfdt
	rec.MFHDGaps = manifest.MFHDGaps
	rec.TruncatedBoxes = manifest.TruncatedBoxes
	rec.RecoveredFragments = manifest.RecoveredFragments
	rec.SHA256 = manifest.OutputSHA256
	rec.ManifestSHA256 = digest
	rec.OutputPath = mediaPath
	rec.ManifestPath = manifestPath
	rec.IndexPath = indexPath
	rec.Repairs = manifest.Repairs
	rec.Monotonic = isMonotonic(outcome.Index)
	rec.DamagedIndex = manifest.DamagedIndex
	rec.CorruptIndexHints = len(manifest.IndexIssues)
	if rtpReport != nil {
		rec.RTPWraps = rtpReport.Wraps
	}
	return rec
}

// demoRTP synthesises RTP timestamps from the delivered video track and
// validates the modular unwrap against the index timeline. It deliberately
// starts near the 32-bit wrap point so the wrap path is always exercised.
func demoRTP(tracks []model.Track, ix *model.Index) *model.RTPReport {
	if ix == nil || ix.Len() == 0 {
		return nil
	}
	var videoTrack uint32
	for _, t := range tracks {
		if t.Handler == "vide" {
			videoTrack = t.TrackID
			break
		}
	}
	if videoTrack == 0 {
		if len(tracks) == 0 {
			return nil
		}
		videoTrack = tracks[0].TrackID
	}
	entries := ix.Sorted()
	frames := make([]timeline.RTPFrame, 0, len(entries))
	expected := make([]model.Micros, 0, len(entries))
	// Start 3 seconds before the 2^32 wrap at 90 kHz.
	const clock = timeline.RTPClockRate
	wrapPoint := uint64(1) << 32
	lead := uint64(3) * uint64(clock)
	base := wrapPoint - lead
	// Anchor the RTP clock at the video track's own first entry; entries are
	// sorted track-first, so entries[0] may belong to another track.
	var origin model.Micros
	haveOrigin := false
	for _, e := range entries {
		if e.TrackID != videoTrack {
			continue
		}
		if !haveOrigin {
			origin = e.StartMicros
			haveOrigin = true
		}
		offsetTicks := uint64(e.StartMicros-origin) * uint64(clock) / uint64(model.MicrosPerSecond)
		ts := uint32((base + offsetTicks) & 0xFFFFFFFF)
		frames = append(frames, timeline.RTPFrame{
			SequenceNumber: uint16(len(frames)),
			Timestamp:      ts,
			Marker:         true,
		})
		// ValidateRTP maps timestamps relative to the first packet, so the
		// expected timeline must be relative to the same origin.
		expected = append(expected, e.StartMicros-origin)
	}
	if len(frames) == 0 {
		return nil
	}
	rep, err := timeline.ValidateRTP(frames, clock, expected, 2_000)
	if err != nil {
		return &model.RTPReport{Matched: false}
	}
	return rep
}

// isMonotonic reports whether every track's index entries are ordered in time
// without overlap.
func isMonotonic(ix *model.Index) bool {
	last := make(map[uint32]model.Micros, 4)
	for _, e := range ix.Sorted() {
		if prev, ok := last[e.TrackID]; ok && e.StartMicros < prev {
			return false
		}
		last[e.TrackID] = e.EndMicros
	}
	return true
}
