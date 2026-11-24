package pipeline

import (
	"encoding/json"
	"io"

	"archive/internal/clock"
)

// PrintSummary writes the demo summary as indented JSON plus a short
// human-readable verdict on stderr-free stdout.
func PrintSummary(w io.Writer, sum *Summary) error {
	b, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// VerifyAcceptance checks the demo invariants that the CLI promises. It is
// exported so tests can reuse the exact acceptance rules.
func VerifyAcceptance(sum *Summary) error {
	if len(sum.Normal.RecoveredLayers) < 3 {
		return clock.Newf(clock.KindMisaligned, "normal sample recovered %d layers, want at least 3", len(sum.Normal.RecoveredLayers))
	}
	if sum.Normal.Duplicates <= 0 {
		return clock.New(clock.KindInvalidMetadata, "normal sample counted no duplicate packets")
	}
	if sum.Normal.Lost != 0 {
		return clock.Newf(clock.KindMisaligned, "normal sample lost %d packets, want 0", sum.Normal.Lost)
	}
	if sum.Burst.ActiveLayers < 2 {
		return clock.Newf(clock.KindMisaligned, "burst sample recovered %d layers, want at least 2", sum.Burst.ActiveLayers)
	}
	if sum.Burst.Lost <= 0 {
		return clock.New(clock.KindInvalidMetadata, "burst sample should retain missing markers")
	}
	if sum.BoundaryErrorNS > 1 {
		return clock.Newf(clock.KindMisaligned, "boundary error %d ns exceeds 1 ns", sum.BoundaryErrorNS)
	}
	if sum.CorruptError == "" || sum.Corrupt.ExitCode == 0 {
		return clock.New(clock.KindInvalidMetadata, "corrupt sample did not fail with a located error")
	}
	if sum.BitrateDeviationPct > 5 {
		return clock.Newf(clock.KindMisaligned, "bitrate deviation %.2f%% exceeds 5%%", sum.BitrateDeviationPct)
	}
	if sum.Normal.QualityPSNRDb < 30 {
		return clock.Newf(clock.KindMisaligned, "quality %.2f dB below 30 dB", sum.Normal.QualityPSNRDb)
	}
	return nil
}
