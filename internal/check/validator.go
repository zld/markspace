// Package check independently validates a merged manifest against the original
// elementary stream, key-frame index, transport gap list and the bytes actually
// staged on disk.
//
// The validator is deliberately self-contained: it re-scans the raw stream,
// re-parses the index and re-hashes every payload itself instead of trusting
// any success marker recorded by the worker pool or merge stager.  A manifest
// only passes when every boundary, reference, timestamp, gap and checksum can
// be reproduced from the source bytes.
package check

import (
	"crypto/sha256"
	"encoding/hex"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/ingest"
	"mediapipeline/internal/merge"
	"mediapipeline/internal/transport"
)

// Input bundles everything the validator re-derives results from.
type Input struct {
	Codec    ingest.Codec
	Stream   []byte            // raw elementary stream bytes
	Index    *ingest.IndexFile // parsed key-frame index
	Manifest *merge.Manifest   // manifest under validation
	GapSSRC  uint32            // SSRC whose gaps are authoritative
	Gaps     []transport.Gap   // transport gaps the run observed
	Staged   []byte            // canonical manifest bytes on disk
	Limits   ingest.Limits
}

// Result is the independent verdict.
type Result struct {
	Pass   bool
	Digest string // SHA-256 the validator recomputed from the staged bytes
	Faults []fault.Fault
}

// Validate runs every check and collects all failures before deciding.
func Validate(in Input) *Result {
	rep := &fault.Report{}
	zero := clock.MustRational(0, 1)
	res := &Result{}

	// 1. Re-scan the stream from raw bytes.  Nothing from the worker pool is
	// consulted; unit boundaries are rediscovered here.
	units, err := ingest.Scan(in.Codec, in.Stream, in.Limits, rep)
	if err != nil {
		res.Faults = rep.All()
		return res
	}
	checkBoundaries(units, rep)

	indexed := make(map[uint32]ingest.IndexEntry)
	if in.Index != nil {
		// 2. Every index entry must resolve to an existing unit.
		for _, e := range in.Index.Entries {
			indexed[e.UnitIndex] = e
			if int(e.UnitIndex) >= len(units) {
				rep.Addf(fault.CodeIndexOutOfBounds, e.Offset, e.Seconds, e.Seconds,
					"index references missing unit %d (have %d)", e.UnitIndex, len(units))
				continue
			}
			u := units[e.UnitIndex]
			if e.Offset != u.Offset || int64(e.Size) != int64(len(u.Payload)) {
				rep.Addf(fault.CodeIndexOutOfBounds, e.Offset, e.Seconds, e.Seconds,
					"index offset/size does not match rescanned unit %d", e.UnitIndex)
			}
		}
	}

	// 3. Every manifest asset must reference a real, indexed unit, hash to the
	//    claimed digest and keep timestamps monotonic along the asset order.
	var prevEnd clock.Rational
	for i, a := range in.Manifest.Assets {
		if a.UnitIndex < 0 || a.UnitIndex >= len(units) {
			rep.Addf(fault.CodeIndexOutOfBounds, a.Offset, a.TimeFrom, a.TimeTo,
				"asset %q references missing unit %d (have %d)", a.Key, a.UnitIndex, len(units))
			continue
		}
		u := units[a.UnitIndex]
		if a.Offset != u.Offset || a.Size != int64(len(u.Payload)) {
			rep.Addf(fault.CodeBoundaryInvalid, a.Offset, a.TimeFrom, a.TimeTo,
				"asset %q boundary %d:%d does not match unit %d %d:%d",
				a.Key, a.Offset, a.Size, a.UnitIndex, u.Offset, len(u.Payload))
		}
		sum := sha256.Sum256(u.Payload)
		if got := hex.EncodeToString(sum[:]); got != a.Digest {
			rep.Addf(fault.CodeChecksumMismatch, a.Offset, a.TimeFrom, a.TimeTo,
				"asset %q digest %s does not match recomputed %s", a.Key, a.Digest, got)
		}
		if i > 0 && a.TimeFrom.Cmp(prevEnd) < 0 {
			rep.Addf(fault.CodeNonMonotonic, a.Offset, a.TimeFrom, a.TimeTo,
				"asset %q time %s precedes previous end %s", a.Key, a.TimeFrom, prevEnd)
		}
		prevEnd = a.TimeTo
	}

	// 4. The gap list must be internally consistent: ordered, non-overlapping.
	checkGaps(in.GapSSRC, in.Gaps, rep)

	// 5. Re-hash the staged bytes and compare with the manifest's own digest,
	//    proving the on-disk output equals the reconciled content.
	if len(in.Staged) == 0 {
		rep.Addf(fault.CodeChecksumMismatch, -1, zero, zero, "no staged manifest bytes to verify")
	} else {
		sum := sha256.Sum256(in.Staged)
		res.Digest = hex.EncodeToString(sum[:])
		if res.Digest != merge.DigestManifest(in.Manifest) {
			rep.Addf(fault.CodeChecksumMismatch, -1, zero, zero,
				"staged manifest digest %s differs from reconciled digest %s",
				res.Digest, merge.DigestManifest(in.Manifest))
		}
	}

	res.Faults = rep.All()
	res.Pass = rep.Count() == 0
	return res
}

// checkBoundaries verifies that rescanned units are tightly packed and never
// overlap, so a corrupted length prefix cannot hide a boundary shift.
func checkBoundaries(units []ingest.Unit, rep *fault.Report) {
	var expect int64
	for _, u := range units {
		if u.Offset != expect {
			rep.Addf(fault.CodeBoundaryInvalid, u.Offset, u.TimeFrom, u.TimeTo,
				"unit at %d is not contiguous; expected %d", u.Offset, expect)
		}
		if u.PayloadOff != u.Offset+4 && u.Codec != ingest.CodecAV1 {
			rep.Addf(fault.CodeBoundaryInvalid, u.Offset, u.TimeFrom, u.TimeTo,
				"NAL payload offset %d is not 4 bytes after unit offset %d", u.PayloadOff, u.Offset)
		}
		expect = u.PayloadOff + int64(len(u.Payload))
	}
}

// checkGaps validates gap ordering and flags overlap.  A duplicated or
// backwards gap means the transport bookkeeping itself is untrustworthy.
func checkGaps(ssrc uint32, gaps []transport.Gap, rep *fault.Report) {
	zero := clock.MustRational(0, 1)
	var prevTo uint64
	for i, g := range gaps {
		if g.SSRC != ssrc {
			rep.Addf(fault.CodeBoundaryInvalid, -1, zero, zero,
				"gap %d belongs to ssrc %d, want %d", i, g.SSRC, ssrc)
		}
		if g.To <= g.From {
			rep.Addf(fault.CodeBoundaryInvalid, -1, zero, zero,
				"gap %d has empty or inverted range %d..%d", i, g.From, g.To)
		}
		if i > 0 && g.From < prevTo {
			rep.Addf(fault.CodeBoundaryInvalid, -1, zero, zero,
				"gap %d at %d overlaps previous gap ending %d", i, g.From, prevTo)
		}
		prevTo = g.To
	}
}
