package pipeline

import (
	"fmt"
	"time"

	"mediakey/internal/issuer"
	"mediakey/internal/reason"
	"mediakey/internal/replay"
	"mediakey/internal/report"
)

func failResult(idx int, keyID string, code reason.Code, format string, args ...any) report.SegmentResult {
	return report.SegmentResult{
		SegmentIndex: idx, KeyID: keyID, Status: report.StatusFailed, Reason: code,
		Detail: fmt.Sprintf(format, args...),
	}
}

// ValidateOptions carries the process-held secrets and state needed to
// authenticate an asset offline.
type ValidateOptions struct {
	// Secret is the license signing key held by this process.
	Secret []byte
	// Now is the validation instant. It comes from the injected clock so
	// license and key expiry boundaries move together.
	Now time.Time
	// Distributor holds key versions including retired history.
	Distributor *issuer.Distributor
	// Replay, when non-nil, atomically consumes the license serial.
	Replay *replay.Cache
	// AssetID is the asset being opened; the license must cover it.
	AssetID string
}

// Outcome is the full classification of one validation attempt.
type Outcome struct {
	License  report.LicenseResult
	Manifest report.ManifestCheck
	Segments []report.SegmentResult
}

// Failed reports whether any part of the outcome failed.
func (o *Outcome) Failed() bool {
	if o.License.Status != report.StatusOK && o.License.Status != "" {
		return true
	}
	if o.Manifest.Checked && o.Manifest.Status == report.StatusFailed {
		return true
	}
	for _, s := range o.Segments {
		if s.Reason.IsFailure() {
			return true
		}
	}
	return false
}
