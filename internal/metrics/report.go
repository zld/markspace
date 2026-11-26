// Package metrics renders the per-record mapping and the run summary,
// and computes the deterministic SHA-256 digest of the mapping.
package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"continuity/internal/core"
)

// Line renders one mapping row in canonical form (also the digest input).
func Line(it core.Item) string {
	return fmt.Sprintf("map idx=%d source=%s line=%d output=%d decision=%s media_ref=%d expected_ref=%d time_delta_us=%d byte_offset=%d byte_delta=%d delay_ms=%d sha256=%s note=%q",
		it.Index, it.Source, it.Line, it.Output, it.Decision,
		it.MediaRef, it.ExpectedRef, it.TimeDeltaUs,
		it.ByteOffset, it.ByteDelta, it.DelayMs,
		hex.EncodeToString(it.Fingerprint[:]), it.Note)
}

// Digest returns the SHA-256 (64 hex chars) over canonical mapping lines.
// Identical inputs and seed always yield the same digest.
func Digest(items []core.Item) string {
	h := sha256.New()
	for _, it := range items {
		h.Write([]byte(Line(it)))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SummaryLine renders the aggregate summary including the digest.
func SummaryLine(s core.Summary, digest string) string {
	return fmt.Sprintf("summary mapped=%d emitted=%d duplicate=%d lost=%d late=%d resumed=%d gap_ms=%d p50_delay_ms=%d drift_ppm=%d max_abs_time_delta_us=%d max_abs_byte_delta=%d digest=%s",
		s.Mapped, s.Emitted, s.Duplicate, s.Lost, s.Late, s.Resumed,
		s.GapMs, s.P50DelayMs, s.DriftPPM,
		s.MaxAbsTimeDeltaUs, s.MaxAbsByteDelta, digest)
}

// Report writes all mapping rows followed by the summary line.
func Report(w io.Writer, res *core.Result) {
	for _, it := range res.Items {
		fmt.Fprintln(w, Line(it))
	}
	fmt.Fprintln(w, SummaryLine(res.Summary, Digest(res.Items)))
}
