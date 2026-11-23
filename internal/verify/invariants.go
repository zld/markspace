// Package verify checks the invariants that must hold across every stage and
// produces a deterministic digest of the audited result.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"archivecheck/internal/manifest"
	"archivecheck/internal/recovery"
)

// Invariant names are stable identifiers used in audit output.
const (
	TimeMonotonic   = "time_monotonic"
	OffsetBounded   = "offset_bounded"
	GapsExplained   = "gaps_explained"
	BoundaryAligned = "boundary_aligned"
	// SourceMonotonic and SourceBounded are the same invariants applied to the
	// pre-recovery evidence, so damage cannot be introduced by recovery itself.
	SourceMonotonic = "source_time_monotonic"
	SourceBounded   = "source_offset_bounded"
)

// Source is the pre-recovery evidence of one layer: the units the parser saw
// and the stream length they must fit inside.
type Source struct {
	Layer     string
	StreamLen int64
	Units     []recovery.Unit
}

// Violation is one failed invariant with machine-readable evidence.
type Violation struct {
	Invariant string
	Layer     string
	Detail    string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s layer=%s detail=%s", v.Invariant, v.Layer, v.Detail)
}

// Report is the verifier outcome.
type Report struct {
	Checked    []string
	Violations []Violation
	Faults     []string
	SHA256     string
}

// OK reports whether every invariant held.
func (r Report) OK() bool { return len(r.Violations) == 0 }

// Text renders a stable terminal summary.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "verify checked=%s violations=%d\n", strings.Join(r.Checked, ","), len(r.Violations))
	for _, v := range r.Violations {
		fmt.Fprintf(&b, "  violation %s\n", v)
	}
	fmt.Fprintf(&b, "verify_sha256=%s\n", r.SHA256)
	return b.String()
}

// Verify checks per-layer and merged results against the three required
// invariants plus boundary alignment. The digest covers the canonical
// violation list and manifest digest so identical runs must produce identical
// digests.
func Verify(results []recovery.Result, merge recovery.Merge, doc manifest.Document) Report {
	return VerifySources(results, merge, doc, nil)
}

// VerifySources checks both the pre-recovery evidence and the recovered
// result. Passing no sources checks only the recovered result.
func VerifySources(results []recovery.Result, merge recovery.Merge, doc manifest.Document, sources []Source) Report {
	checked := []string{TimeMonotonic, OffsetBounded, GapsExplained, BoundaryAligned}
	if len(sources) > 0 {
		checked = append(checked, SourceMonotonic, SourceBounded)
	}
	rep := Report{Checked: checked}
	for _, src := range sources {
		rep.Violations = append(rep.Violations, checkSource(src)...)
	}
	for _, r := range results {
		rep.Violations = append(rep.Violations, checkTime(r)...)
		rep.Violations = append(rep.Violations, checkOffsets(r)...)
		rep.Violations = append(rep.Violations, checkGaps(r)...)
	}
	rep.Violations = append(rep.Violations, checkBoundaries(merge)...)
	sort.SliceStable(rep.Violations, func(i, j int) bool {
		a, b := rep.Violations[i], rep.Violations[j]
		if a.Invariant != b.Invariant {
			return a.Invariant < b.Invariant
		}
		if a.Layer != b.Layer {
			return a.Layer < b.Layer
		}
		return a.Detail < b.Detail
	})
	rep.Faults = append(rep.Faults, doc.Faults...)
	for _, f := range merge.Faults {
		rep.Faults = append(rep.Faults, f.String())
	}
	sort.Strings(rep.Faults)
	rep.SHA256 = digest(rep.Violations, doc.SHA256, evidence(results, merge))
	return rep
}

// evidence renders the audited coverage in a canonical order so the digest
// changes whenever recovered spans, anchors, gaps or alignment change.
func evidence(results []recovery.Result, merge recovery.Merge) []string {
	layers := make([]recovery.Result, len(results))
	copy(layers, results)
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].Layer < layers[j].Layer })
	var out []string
	for _, r := range layers {
		out = append(out, fmt.Sprintf("layer=%s start=%d end=%d ratio=%.6f mean=%.6f max=%d", r.Layer, r.StartMs, r.EndMs, r.ParseRatio, r.MeanKFMs, r.MaxKFMs))
		for _, a := range r.Anchors {
			out = append(out, fmt.Sprintf("anchor %s %d %d", r.Layer, a.TimeMs, a.Offset))
		}
		for _, s := range r.Segments {
			out = append(out, fmt.Sprintf("segment %s %d %d %d %d", r.Layer, s.StartMs, s.EndMs, s.ByteStart, s.ByteEnd))
		}
		for _, g := range r.Gaps {
			out = append(out, fmt.Sprintf("gap %s %d %d %s %s", r.Layer, g.StartMs, g.EndMs, g.Reason, g.Detail))
		}
	}
	for _, b := range merge.Boundaries {
		out = append(out, fmt.Sprintf("boundary %d spread=%d %s", b.RefMs, b.Spread, formatTimes(b.Times)))
	}
	return out
}

// checkTime requires anchors and segments to be non-decreasing in time.
func checkTime(r recovery.Result) []Violation {
	var out []Violation
	for i := 1; i < len(r.Anchors); i++ {
		if r.Anchors[i].TimeMs < r.Anchors[i-1].TimeMs {
			out = append(out, Violation{TimeMonotonic, r.Layer,
				fmt.Sprintf("anchor %d at %d ms precedes %d ms", i, r.Anchors[i].TimeMs, r.Anchors[i-1].TimeMs)})
		}
		if r.Anchors[i].Offset < r.Anchors[i-1].Offset {
			out = append(out, Violation{TimeMonotonic, r.Layer,
				fmt.Sprintf("anchor %d byte offset %d precedes %d", i, r.Anchors[i].Offset, r.Anchors[i-1].Offset)})
		}
	}
	for i := 1; i < len(r.Segments); i++ {
		if r.Segments[i].StartMs < r.Segments[i-1].EndMs {
			out = append(out, Violation{TimeMonotonic, r.Layer,
				fmt.Sprintf("segment %d starts at %d ms before segment %d ends at %d ms", i, r.Segments[i].StartMs, i-1, r.Segments[i-1].EndMs)})
		}
	}
	return out
}

// checkOffsets requires every segment and gap to stay inside the stream.
func checkOffsets(r recovery.Result) []Violation {
	var out []Violation
	check := func(kind string, start, end int64) {
		if start < 0 || end > r.StreamLen || end < start {
			out = append(out, Violation{OffsetBounded, r.Layer,
				fmt.Sprintf("%s bytes [%d,%d) outside stream bounds [0,%d]", kind, start, end, r.StreamLen)})
		}
	}
	for _, s := range r.Segments {
		check("segment", s.ByteStart, s.ByteEnd)
	}
	for _, g := range r.Gaps {
		check("gap", g.ByteStart, g.ByteEnd)
	}
	return out
}

// checkGaps requires every retained hole to carry a non-empty, attributable
// reason. An unexplained gap is a hard failure.
func checkGaps(r recovery.Result) []Violation {
	var out []Violation
	for i, g := range r.Gaps {
		if g.EndMs <= g.StartMs {
			out = append(out, Violation{GapsExplained, r.Layer,
				fmt.Sprintf("gap %d has non-positive duration [%d,%d)", i, g.StartMs, g.EndMs)})
		}
		if g.Reason == "" || g.Detail == "" {
			out = append(out, Violation{GapsExplained, r.Layer,
				fmt.Sprintf("gap %d at %d ms lacks an attributable reason", i, g.StartMs)})
		}
	}
	if r.ParseRatio < 0 || r.ParseRatio > 1 {
		out = append(out, Violation{GapsExplained, r.Layer, fmt.Sprintf("parse ratio %.4f outside [0,1]", r.ParseRatio)})
	}
	return out
}

// checkSource requires parsed units to lie inside the stream and to advance in
// both byte offset and (implicitly) discovery order before recovery runs.
func checkSource(src Source) []Violation {
	var out []Violation
	var prevOff, prevEnd int64 = -1, -1
	for i, u := range src.Units {
		if u.Truncated {
			// A truncated unit's declared end may point past the surviving
			// bytes; only its start must be inside the stream.
			if u.Offset < 0 || u.Offset >= src.StreamLen {
				out = append(out, Violation{SourceBounded, src.Layer,
					fmt.Sprintf("truncated unit %d starts at %d outside stream [0,%d)", i, u.Offset, src.StreamLen)})
			}
			prevOff, prevEnd = u.Offset, u.End
			continue
		}
		if u.Offset < 0 || u.End < u.Offset || u.End > src.StreamLen {
			out = append(out, Violation{SourceBounded, src.Layer,
				fmt.Sprintf("unit %d bytes [%d,%d) outside stream bounds [0,%d]", i, u.Offset, u.End, src.StreamLen)})
		}
		if u.Offset < prevOff {
			out = append(out, Violation{SourceMonotonic, src.Layer,
				fmt.Sprintf("unit %d offset %d precedes prior unit offset %d", i, u.Offset, prevOff)})
		} else if prevOff >= 0 && u.Offset < prevEnd {
			out = append(out, Violation{SourceMonotonic, src.Layer,
				fmt.Sprintf("unit %d starts at %d inside prior unit ending at %d", i, u.Offset, prevEnd)})
		}
		if u.End < prevEnd && u.Offset >= prevOff {
			out = append(out, Violation{SourceMonotonic, src.Layer,
				fmt.Sprintf("unit %d ends at %d before prior unit end %d", i, u.End, prevEnd)})
		}
		prevOff, prevEnd = u.Offset, u.End
	}
	return out
}

func checkBoundaries(m recovery.Merge) []Violation {
	var out []Violation
	for _, c := range m.Conflicts {
		if c.Spread <= c.Tolerance {
			out = append(out, Violation{BoundaryAligned, "",
				fmt.Sprintf("conflict at %d ms reported with spread %d inside tolerance %d", c.RefMs, c.Spread, c.Tolerance)})
		}
	}
	if m.ToleranceMs <= 0 {
		out = append(out, Violation{BoundaryAligned, "", "merge tolerance is not positive"})
	}
	return out
}

// Digest hashes the canonical violation evidence against a manifest digest.
func Digest(rep Report, manifestSHA string) (string, error) {
	return digest(rep.Violations, manifestSHA, nil), nil
}

func digest(vs []Violation, manifestSHA string, ev []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "manifest=%s\n", manifestSHA)
	for _, v := range vs {
		fmt.Fprintf(h, "%s|%s|%s\n", v.Invariant, v.Layer, v.Detail)
	}
	for _, e := range ev {
		fmt.Fprintf(h, "evidence|%s\n", e)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func formatTimes(times map[string]int64) string {
	keys := make([]string, 0, len(times))
	for k := range times {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%d", k, times[k])
	}
	return b.String()
}
