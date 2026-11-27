package pipeline

import (
	"path/filepath"
	"time"

	"mediacore/internal/media"
	"mediacore/internal/quality"
)

// stageQuality encodes the primary and a deliberately degraded rendition,
// measures both, and applies the fixed quality/budget/size decision.
func stageQuality(tool *media.Tool, rep *Report, opts Options, syn media.Synthetic, ref, encoded string) error {
	encodeStart := time.Now()
	ft, err := tool.EncodeTarget(ref, encoded, string(opts.Codec), syn, opts.TargetKbps, opts.Preset)
	if err != nil {
		return err
	}
	processMS := time.Since(encodeStart).Milliseconds()
	rep.FineTunePasses = ft.Iterations
	psnr, err := tool.MeasurePSNR(ref, encoded, syn)
	if err != nil {
		return err
	}
	degraded := filepath.Join(filepath.Dir(encoded), "degraded."+string(opts.Codec))
	if err := tool.EncodeDegraded(ref, degraded, string(opts.Codec), syn, opts.Width/4); err != nil {
		return err
	}
	degradedPSNR, err := tool.MeasurePSNR(ref, degraded, syn)
	if err != nil {
		return err
	}
	encBytes, err := fileSize(encoded)
	if err != nil {
		return err
	}
	degBytes, err := fileSize(degraded)
	if err != nil {
		return err
	}
	qcfg := quality.Config{
		TargetKbps: opts.TargetKbps, QualityMin: opts.QualityMin,
		Budget: opts.BudgetMS, RateTolerance: quality.DefaultRateTolerance,
	}
	cands := []quality.Candidate{
		{Name: "degraded", Codec: string(opts.Codec), SizeBytes: degBytes, DurationSec: syn.Duration(), PSNRdB: degradedPSNR, ProcessMS: processMS / 3},
		{Name: "primary", Codec: string(opts.Codec), SizeBytes: encBytes, DurationSec: syn.Duration(), PSNRdB: psnr, ProcessMS: processMS},
	}
	dec, err := quality.Decide(qcfg, cands)
	if err != nil {
		rep.Decision = "reject"
		if dec != nil {
			rep.DecisionReason = dec.Reason
		} else {
			rep.DecisionReason = err.Error()
		}
		rep.computeDigest()
		return err
	}
	rep.Decision = "accept"
	rep.DecisionReason = dec.Reason
	summary := quality.Summarize(qcfg, *dec.Selected, dec.Reason)
	rep.ActualKbps = round2(summary.ActualKbps)
	rep.DeviationPct = round2(summary.DeviationPct)
	rep.PSNRdB = round2(summary.PSNRdB)
	rep.ProcessMS = summary.ProcessMS
	return nil
}
