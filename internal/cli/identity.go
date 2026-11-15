package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Deterministic fixture geometry. Small planes keep the offline demo fast
// while still exercising quantization, RLE and every PSNR edge case.
const (
	fixtureFPS    = 10
	fixtureWidth  = 16
	fixtureHeight = 12
)

// jobID derives a stable identifier from the run parameters alone, so the
// same inputs always map to the same resumable job regardless of where the
// workspace happens to live on disk. Path-specific state stays in the
// checkpoint Params and the input fingerprint check.
func jobID(o Options) string {
	h := sha256.New()
	fmt.Fprintf(h, "%g|%g|%g|%g|%d|%d",
		o.DurationSec, o.SegmentSec, o.TargetKbps, o.MinKbps,
		o.Workers, o.MaxOutputBytes)
	return "job-" + hex.EncodeToString(h.Sum(nil))[:12]
}
