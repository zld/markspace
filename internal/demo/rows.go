package demo

import (
	"context"
	"math"

	"mediad/internal/fixture"
	"mediad/internal/ingest"
	"mediad/internal/model"
	"mediad/internal/pipeline"
)

// rowFromArchive builds the acceptance row for a successfully archived source
// by reading the committed manifest entry and quality report.
func (p *Processor) rowFromArchive(sourceSHA string, s *fixture.Sample, stats *ingest.Stats, stage string) *Row {
	entry := p.manifest.Get(sourceSHA)
	row := &Row{
		SourceSHA:        sourceSHA,
		Codec:            s.Codec,
		TargetBPS:        s.TargetBPS,
		ManifestStartNS:  entry.StartNS,
		ManifestEndNS:    entry.EndNS,
		AlignmentErrorNS: entry.MaxAlignErrNS,
		BufferPeakBytes:  int64(p.cfg.PeakBytes()),
		Stage:            stage,
		ExitCode:         model.ExitOK,
		Case:             "success",
	}
	// Metrics are recomputed deterministically from the same fixture frames.
	metrics, err := computeMetrics(s)
	if err == nil {
		row.ActualBPS = metrics["actual_bps"].(int64)
		row.BitrateErrorPct = metrics["bitrate_error_pct"].(float64)
		row.PSNRDB = metrics["psnr_db"].(float64)
		row.SSIM = metrics["ssim"].(float64)
	}
	if stats != nil && stats.PeakInFlight > 0 && stats.PeakInFlight < row.BufferPeakBytes {
		row.BufferPeakBytes = stats.PeakInFlight
	}
	return row
}

// ProcessCorrupt validates a deliberately corrupted fixture and returns a row
// carrying the structured error code and first reportable offset. It never
// produces a success manifest entry, and the row's exit code is always 2.
func (p *Processor) ProcessCorrupt(c fixture.CorruptSample) *Row {
	row := &Row{
		Codec:           c.Codec,
		TargetBPS:       0,
		ManifestStartNS: 0,
		ManifestEndNS:   0,
		BufferPeakBytes: int64(p.cfg.PeakBytes()),
		Stage:           pipeline.StageIngest,
		ExitCode:        model.ExitCorrupt,
		Case:            string(c.Kind),
		ErrorCode:       c.WantCode,
		ErrorOffset:     -1,
	}
	// Exercise the real validation path so the reported code/offset are
	// produced by production code, not copied from the fixture.
	err := p.detectCorruption(c)
	if se, ok := model.AsStructured(err); ok {
		row.ErrorCode = se.Code
		row.ErrorOffset = se.ByteOffset
		row.SourceSHA = "invalid-" + string(c.Kind)
	} else if err != nil {
		row.ErrorCode = model.ErrStageState
	}
	// Zero-timebase has no byte position; leave offset at -1.
	if c.Kind == fixture.CorruptZeroTimebase {
		row.ErrorOffset = -1
	}
	return row
}

// detectCorruption runs the appropriate production validator for a corrupt
// variant and returns its structured error.
func (p *Processor) detectCorruption(c fixture.CorruptSample) error {
	switch c.Kind {
	case fixture.CorruptIndexOutOfRange:
		_, err := ingest.ValidateIndex(c.Codec, c.Index.Entries, int64(len(c.Stream)))
		return err
	case fixture.CorruptZeroTimebase:
		return c.FPS.Validate(false)
	case fixture.CorruptTruncatedUnit, fixture.CorruptIllegalHeader:
		sc := ingest.NewScanner(p.cfg)
		_, err := sc.Scan(context.Background(), c.Codec, c.Stream)
		return err
	default:
		return model.New(model.ErrStageState, c.Codec, -1, "unknown corrupt kind %q", c.Kind)
	}
}

// CheckAcceptance evaluates the numeric acceptance bars for one success row.
// It returns the list of violated expectations (empty means all pass).
func CheckAcceptance(r *Row) []string {
	var fails []string
	if r.ExitCode != model.ExitOK {
		fails = append(fails, "non-zero exit code")
		return fails
	}
	if r.AlignmentErrorNS > 1 {
		fails = append(fails, "alignment error exceeds 1 ns")
	}
	if r.BufferPeakBytes > int64(peakLimit) {
		fails = append(fails, "buffer peak exceeds configured ceiling")
	}
	if r.BitrateErrorPct > 5.0 {
		fails = append(fails, "bitrate error exceeds 5%")
	}
	if r.PSNRDB < 30.0 {
		fails = append(fails, "PSNR below 30 dB")
	}
	if r.SSIM < 0.95 {
		fails = append(fails, "SSIM below 0.95")
	}
	if math.IsNaN(r.SSIM) || math.IsNaN(r.PSNRDB) {
		fails = append(fails, "non-finite metric")
	}
	return fails
}

// peakLimit mirrors the configured 5*64KiB ceiling.
const peakLimit = (defaultQueueDepth + 1) * defaultChunkSize
