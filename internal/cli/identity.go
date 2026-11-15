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

// jobID derives a stable identifier from the run parameters so the same
// inputs always map to the same resumable job.
func jobID(o Options) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%g|%g|%g|%g|%d",
		o.InputPath, o.DurationSec, o.SegmentSec,
		o.TargetKbps, o.MinKbps, o.Workers)
	return "job-" + hex.EncodeToString(h.Sum(nil))[:12]
}
