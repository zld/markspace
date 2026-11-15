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

// jobID derives a stable identifier from the output-determining parameters
// alone, so the same material maps to the same resumable job regardless of
// where the workspace lives or how many workers are used. Scheduling
// resources (workers, size ceiling) and paths intentionally do not enter it.
func jobID(o Options) string {
	h := sha256.New()
	fmt.Fprintf(h, "%g|%g|%g|%g",
		o.DurationSec, o.SegmentSec, o.TargetKbps, o.MinKbps)
	return "job-" + hex.EncodeToString(h.Sum(nil))[:12]
}
