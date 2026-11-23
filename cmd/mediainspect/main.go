// Command mediainspect analyzes locally generated frame sequences: it scans
// the stream into a digest-bound index, executes reorder/damage-localization
// with metadata normalization, and reports bitrate, quality and manifest
// alignment statistics on the unified media timebase.
//
// Exit codes: 0 = clean run; 2 = input corruption found and localized;
// 1 = parameter or unrecoverable input error.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"mediainspect/internal/media"
	"mediainspect/internal/metrics"
	"mediainspect/internal/normalize"
	"mediainspect/internal/scan"
	"mediainspect/internal/stream"
)

const frameMs = 1000.0 * scan.FrameTicks / media.Timescale

type report struct {
	FramesTotal           int                   `json:"frames_total"`
	KeyframeGapP95Ms      float64               `json:"keyframe_gap_p95_ms"`
	CorruptRanges         []stream.CorruptRange `json:"corrupt_ranges"`
	CorruptRangesCount    int                   `json:"corrupt_ranges_count"`
	RecoveredFrames       int                   `json:"recovered_frames"`
	DuplicateDropped      int                   `json:"duplicate_dropped"`
	LateDropped           int                   `json:"late_dropped"`
	LostFrames            int                   `json:"lost_frames"`
	DamagedFrames         int                   `json:"damaged_frames"`
	TargetKbps            float64               `json:"target_kbps"`
	ActualKbps            float64               `json:"actual_kbps"`
	BitrateDeviationPct   float64               `json:"bitrate_deviation_pct"`
	QualityPSNRMeanDB     float64               `json:"quality_psnr_mean_db"`
	ManifestAlignmentMax  float64               `json:"manifest_alignment_max_ms"`
	IndexDigest           string                `json:"index_digest"`
	StructureSummary      string                `json:"structure_summary"`
	ChecksumErrors        int                   `json:"checksum_errors"`
	TruncationErrors      int                   `json:"truncation_errors"`
	AudioLayoutErrors     int                   `json:"audio_layout_errors"`
	SampleRateErrors      int                   `json:"sample_rate_errors"`
	ColorErrors           int                   `json:"color_errors"`
	ColorAnomalies        int                   `json:"color_anomalies"`
	StaleIndexEvents      int                   `json:"stale_index_events"`
	IndexRebuilt          bool                  `json:"index_rebuilt"`
	InvariantsOK          bool                  `json:"invariants_ok"`
	InvariantViolations   []string              `json:"invariant_violations,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 1 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediainspect demo [flags]")
		return 1
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var cfg scan.GenConfig
	var manifestStepMs float64
	fs.Int64Var(&cfg.Seed, "seed", 42, "deterministic sample seed")
	fs.IntVar(&cfg.Frames, "frames", 3000, "number of frames to generate")
	fs.Float64Var(&cfg.TargetKbps, "target-kbps", 1200, "target bitrate in kbps")
	fs.IntVar(&cfg.KeyframeInterval, "keyframe-interval", 30, "frames between keyframes")
	fs.IntVar(&cfg.LossBurst, "loss-burst", 0, "drop N consecutive frames mid-stream")
	fs.IntVar(&cfg.ReorderWindow, "reorder-window", 8, "reorder/shuffle window size")
	fs.IntVar(&cfg.Duplicates, "duplicates", 0, "number of duplicated frames")
	fs.BoolVar(&cfg.CorruptChecksum, "corrupt-checksum", false, "corrupt one frame checksum")
	fs.BoolVar(&cfg.TruncateFrame, "truncate-frame", false, "truncate one frame")
	fs.BoolVar(&cfg.BadAudioLayout, "bad-audio-layout", false, "inject audio layout/rate mismatches")
	fs.BoolVar(&cfg.ColorConflict, "color-conflict", false, "inject color metadata conflicts")
	staleIndex := fs.Bool("stale-index", false, "present an index built from older input")
	fs.Float64Var(&manifestStepMs, "manifest-step-ms", 1000, "manifest segment step in ms")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if cfg.Frames < 100 {
		fmt.Fprintln(os.Stderr, "frames must be >= 100")
		return 1
	}
	cfg = cfg.WithDefaults()

	// Stage 1: scan and digest the input.
	idx, err := scan.BuildIndex(scan.NewReader(scan.NewGenerator(cfg)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		return 1
	}
	rep := report{FramesTotal: idx.Frames, IndexDigest: idx.Digest, StructureSummary: idx.StructureSummary}

	// Stage 1b: persist the index and reload it; a stale or truncated index
	// is rejected and safely rebuilt from the current input.
	var persisted bytes.Buffer
	persistIdx := idx
	if *staleIndex {
		stale, serr := scan.BuildIndex(scan.NewReader(scan.NewGenerator(
			scan.GenConfig{Seed: cfg.Seed + 999, Frames: cfg.Frames})))
		if serr != nil {
			fmt.Fprintln(os.Stderr, "stale fixture:", serr)
			return 1
		}
		persistIdx = stale
	}
	if err := scan.SaveIndex(persistIdx, &persisted); err != nil {
		fmt.Fprintln(os.Stderr, "save index:", err)
		return 1
	}
	loaded, err := scan.LoadIndex(&persisted)
	switch {
	case errors.Is(err, media.ErrIndexTruncated):
		rep.IndexRebuilt = true
	case err != nil:
		fmt.Fprintln(os.Stderr, "load index:", err)
		return 1
	case scan.CheckIndexFresh(loaded, idx.Digest) != nil:
		rep.StaleIndexEvents++
		rep.IndexRebuilt = true
	}
	if rep.IndexRebuilt {
		rebuilt, rerr := scan.BuildIndex(scan.NewReader(scan.NewGenerator(cfg)))
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "rescan:", rerr)
			return 1
		}
		idx = rebuilt
		rep.IndexDigest = idx.Digest
		rep.StructureSummary = idx.StructureSummary
		rep.FramesTotal = idx.Frames
	}

	// Stage 2: stream execution with reorder, damage localization, normalize.
	ex := stream.NewExecutor(cfg.ReorderWindow, cfg.KeyframeInterval,
		scan.FrameTicks, normalize.DefaultAudioTarget)
	var outputs []stream.OutputFrame
	r := scan.NewReader(scan.NewGenerator(cfg))
	for {
		f, err := r.Next()
		if err != nil { // io.EOF at end of input
			break
		}
		outputs = append(outputs, ex.Push(f)...)
	}
	outputs = append(outputs, ex.Flush()...)

	// Stage 3: metrics.
	br := metrics.Bitrate(outputs, cfg.TargetKbps, frameMs)
	qs := metrics.Quality(outputs)
	var keyMs []float64
	var gaps []float64
	var prevKey float64
	for _, o := range outputs {
		if o.Frame.Keyframe && !o.Damaged {
			if len(keyMs) > 0 {
				gaps = append(gaps, o.TimestampMs-prevKey)
			}
			keyMs = append(keyMs, o.TimestampMs)
			prevKey = o.TimestampMs
		}
	}
	manifest := metrics.ManifestAlignment(keyMs, manifestStepMs, br.DurationMs)
	scanDuration := float64(idx.MaxTimestamp-idx.MinTimestamp)/media.Timescale*1000 + frameMs
	inv := metrics.CheckInvariants(idx.Digest, rep.IndexDigest, ex.Stats.InputFrames,
		ex.Stats, scanDuration, br.DurationMs, frameMs)

	rep.CorruptRanges = ex.Ranges()
	if rep.CorruptRanges == nil {
		rep.CorruptRanges = []stream.CorruptRange{}
	}
	rep.CorruptRangesCount = len(rep.CorruptRanges)
	rep.RecoveredFrames = ex.Stats.Recovered
	rep.DuplicateDropped = ex.Stats.Duplicates
	rep.LateDropped = ex.Stats.LateDropped
	rep.LostFrames = ex.Stats.LostFrames
	rep.DamagedFrames = ex.Stats.DamagedFrames
	rep.TargetKbps = br.TargetKbps
	rep.ActualKbps = round3(br.ActualKbps)
	rep.BitrateDeviationPct = round3(br.DeviationPct)
	rep.QualityPSNRMeanDB = round3(qs.PSNRMeanDB)
	rep.ManifestAlignmentMax = round3(manifest.MaxAlignmentMs)
	rep.KeyframeGapP95Ms = round3(p95(gaps))
	rep.ChecksumErrors = ex.Stats.ErrorsByClass[media.ClassChecksum]
	rep.TruncationErrors = ex.Stats.ErrorsByClass[media.ClassTruncation]
	rep.AudioLayoutErrors = ex.Stats.ErrorsByClass[media.ClassAudioLayout]
	rep.SampleRateErrors = ex.Stats.ErrorsByClass[media.ClassSampleRate]
	rep.ColorErrors = ex.Stats.ErrorsByClass[media.ClassColor]
	rep.ColorAnomalies = ex.Stats.AnomalyCount
	rep.InvariantsOK = inv.OK
	rep.InvariantViolations = inv.Violations

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		fmt.Fprintln(os.Stderr, "encode report:", err)
		return 1
	}

	totalErrors := rep.ChecksumErrors + rep.TruncationErrors + rep.AudioLayoutErrors +
		rep.SampleRateErrors + rep.ColorErrors
	if rep.CorruptRangesCount > 0 || totalErrors > 0 || rep.ColorAnomalies > 0 ||
		rep.StaleIndexEvents > 0 {
		return 2
	}
	return 0
}

func p95(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[int(0.95*float64(len(s)-1)+0.5)]
}

func round3(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
}
