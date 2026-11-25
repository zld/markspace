// Package report renders an asset-check run for the terminal and produces a
// stable digest of the run's outcome.
//
// The summary digest is deliberately specified: algorithm fnv1a64/v1 over a
// canonical, newline-separated field list. Two runs over identical inputs
// must produce byte-identical output and therefore the same digest, and any
// change in the mapping, in fault order or in the index digest changes it.
package report

import (
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strings"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/index"
	"example.com/assetcheck/internal/stream"
	"example.com/assetcheck/internal/validate"
)

// SummaryDigestVersion names the digest algorithm and canonicalisation
// version. It is printed with every summary.
const SummaryDigestVersion = index.ReportDigestAlgorithm

// IndexState describes what happened to the on-disk index for this run.
type IndexState struct {
	Path    string
	Loaded  bool
	Rebuilt bool
	Reused  bool
	Stale   bool
	Reason  string
	Digest  index.Digest
	Matched bool
}

// Run bundles everything one asset check produced.
type Run struct {
	Scenario string
	Index    *index.Index
	Scan     []*fault.Error
	Validate *validate.Result
	Stream   *stream.Outcome
	State    IndexState
}

// Verdict summarises the outcome for the process exit code.
type Verdict int

const (
	// Pass means the asset satisfied every constraint.
	Pass Verdict = iota
	// Fail means the asset was checked and did not satisfy them.
	Fail
	// Error means the check could not run.
	Error
)

// VerdictOf derives the verdict from the run.
func (r *Run) Verdict() Verdict {
	if r == nil {
		return Error
	}
	if r.Index == nil {
		return Error
	}
	if r.State.Stale && !r.State.Rebuilt {
		return Error
	}
	if r.Validate != nil && len(r.Validate.Faults) > 0 {
		return Fail
	}
	if r.Stream != nil && !r.Stream.Completed {
		return Fail
	}
	if len(r.Scan) > 0 {
		return Fail
	}
	return Pass
}

// Write renders the full report.
func Write(w io.Writer, r *Run) error {
	if r == nil {
		return fmt.Errorf("report: no run")
	}
	p := &printer{w: w}
	p.line("assetcheck %s", r.Scenario)
	idx := r.Index
	if idx != nil {
		p.line("input digest   %s:%s (%d bytes)", idx.Digest.Algorithm, idx.Digest.Value, idx.Digest.Size)
		for _, in := range idx.Inputs {
			p.line("  %-12s %s:%s (%d bytes)", in.Name, in.Digest.Algorithm, in.Digest.Value, in.Digest.Size)
		}
		switch {
		case r.State.Stale && r.State.Rebuilt:
			p.line("index stale    yes, rebuilt (%s)", r.State.Reason)
		case r.State.Rebuilt:
			p.line("index rebuilt  yes (%s)", r.State.Reason)
		case r.State.Reused:
			p.line("index reused   %s", r.State.Path)
		case r.State.Loaded:
			p.line("index loaded   %s", r.State.Path)
		default:
			p.line("index built    in memory")
		}
		if len(idx.Tracks) > 0 {
			p.line("tracks/frags   %d tracks, %d fragments, %d packets", len(idx.Tracks), len(idx.Fragments), len(idx.Packets))
		}
	}
	if r.Validate != nil {
		p.line("")
		p.line("sample mapping")
		p.table(r.Validate.Mappings)
		p.line("")
		p.line("validation     duplicate=%d missing=%d skew=%v subtitle=%v covered of %v",
			r.Validate.Duplicate, r.Validate.Missing, r.Validate.Skew, r.Validate.Subtitle.Covered, r.Validate.Subtitle.Required)
	}
	if r.Stream != nil {
		p.line("stream         confirmed=%d failed=%d skipped=%d completed=%v total-byte-dev=%d total-time-dev=%v",
			r.Stream.Confirmed, r.Stream.Failed, r.Stream.Skipped, r.Stream.Completed, r.Stream.ByteDev, r.Stream.TimeDev)
	}
	faults := collect(r)
	p.line("")
	if len(faults) == 0 {
		p.line("faults         none")
	} else {
		p.line("faults         %d", len(faults))
		for _, f := range faults {
			p.line("  %s", f.Error())
		}
	}
	digest, canonical := Summarize(r, faults)
	p.line("")
	p.line("summary digest %s:%016x", SummaryDigestVersion, digest)
	p.line("result         %s", verdictName(r.Verdict()))
	_ = canonical
	return p.err
}

// collect merges every fault source into one deterministically ordered list.
func collect(r *Run) []*fault.Error {
	out := make([]*fault.Error, 0, len(r.Scan)+16)
	out = append(out, r.Scan...)
	if r.Validate != nil {
		out = append(out, r.Validate.Faults...)
	}
	if r.Stream != nil {
		out = append(out, r.Stream.Faults...)
	}
	fault.Sort(out)
	return out
}

// Summarize returns the stable summary digest plus the canonical field list
// it was computed over. Only fields that identify the run's outcome are
// included; paths and wall-clock values are excluded on purpose.
func Summarize(r *Run, faults []*fault.Error) (uint64, []string) {
	fields := make([]string, 0, 32+len(faults))
	if r.Index != nil {
		fields = append(fields, "input="+r.Index.Digest.Value)
		fields = append(fields, fmt.Sprintf("tracks=%d", len(r.Index.Tracks)))
		fields = append(fields, fmt.Sprintf("fragments=%d", len(r.Index.Fragments)))
		fields = append(fields, fmt.Sprintf("packets=%d", len(r.Index.Packets)))
		fields = append(fields, fmt.Sprintf("complete=%t", r.Index.Complete))
	}
	if r.Validate != nil {
		for _, m := range r.Validate.Mappings {
			fields = append(fields, fmt.Sprintf("map:%d:%d:%d:%d:%d:%d:%d:%t",
				m.TrackID, m.Sample, m.Offset, m.Packet, m.DTS, m.Dev.ByteDelta, m.Dev.TimeDelta, m.Dev.OK))
		}
		fields = append(fields, fmt.Sprintf("dup=%d", r.Validate.Duplicate))
		fields = append(fields, fmt.Sprintf("missing=%d", r.Validate.Missing))
		fields = append(fields, fmt.Sprintf("skew=%d", r.Validate.Skew))
		fields = append(fields, fmt.Sprintf("subtitle=%d/%d", r.Validate.Subtitle.Covered, r.Validate.Subtitle.Required))
	}
	if r.Stream != nil {
		fields = append(fields, fmt.Sprintf("stream:%d:%d:%d:%t:%d:%d",
			r.Stream.Confirmed, r.Stream.Failed, r.Stream.Skipped, r.Stream.Completed, r.Stream.ByteDev, r.Stream.TimeDev))
	}
	for _, f := range faults {
		fields = append(fields, fmt.Sprintf("fault:%s:%s:%d:%d:%d",
			f.Stage, f.Code, f.Offset, f.Time, f.Sample))
	}
	fields = append(fields, "verdict="+verdictName(r.Verdict()))
	h := fnv.New64a()
	for _, f := range fields {
		_, _ = io.WriteString(h, f)
		_, _ = io.WriteString(h, "\n")
	}
	return h.Sum64(), fields
}

func verdictName(v Verdict) string {
	switch v {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	default:
		return "error"
	}
}

type printer struct {
	w   io.Writer
	err error
}

func (p *printer) line(format string, args ...interface{}) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprintf(p.w, format+"\n", args...)
}

func (p *printer) table(rows []validate.Mapping) {
	if len(rows) == 0 {
		p.line("  (none)")
		return
	}
	p.line("  %-6s %-6s %-7s %-10s %-8s %-10s %-12s %-10s %-12s %s",
		"track", "sample", "offset", "dts", "packet", "time", "byte-dev", "time-dev", "state", "note")
	for _, m := range rows {
		pkt := "-"
		if m.HasPkt {
			pkt = fmt.Sprintf("%d", m.Packet)
		}
		state := "ok"
		if !m.Dev.OK {
			state = "drift"
		}
		p.line("  %-6d %-6d %-7d %-10d %-8s %-10s %-12d %-10d %-12s %s",
			m.TrackID, m.Sample, m.Offset, int64(m.DTS), pkt, timeCell(m.Time),
			m.Dev.ByteDelta, int64(m.Dev.TimeDelta), state, m.Note)
	}
}

func timeCell(t clock.Nanos) string {
	if t == clock.Invalid {
		return "unknown"
	}
	return fmt.Sprintf("%d", int64(t))
}

// SortMappings provides a stable order for callers that merge mappings from
// several runs.
func SortMappings(list []validate.Mapping) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].TrackID != list[j].TrackID {
			return list[i].TrackID < list[j].TrackID
		}
		return list[i].Sample < list[j].Sample
	})
}

// OneLine renders a compact single-line result, used by the demo summary.
func OneLine(r *Run) string {
	v := verdictName(r.Verdict())
	var b strings.Builder
	if r.Index != nil {
		fmt.Fprintf(&b, "digest=%s ", r.Index.Digest.Value[:12])
	}
	if r.Validate != nil {
		fmt.Fprintf(&b, "dup=%d missing=%d faults=%d ", r.Validate.Duplicate, r.Validate.Missing, len(r.Validate.Faults))
	}
	fmt.Fprintf(&b, "result=%s", v)
	return b.String()
}
