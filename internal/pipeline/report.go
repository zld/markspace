package pipeline

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"liveregress/internal/fault"
)

// SegmentReport is one segment's verification outcome.
type SegmentReport struct {
	Layer       string
	Path        string
	Sequence    int64
	InputSHA256 string
	// FileSHA256 is the digest of the whole remuxed segment file; it is the
	// value that must stay identical across reruns.
	FileSHA256 string
	// PayloadSHA256 is the digest of the remuxed sample bytes in order.
	PayloadSHA256 string
	// SourcePayloadSHA256 is the digest of the exact source sample bytes in
	// the same order. Equal payload digests prove the remux was lossless.
	SourcePayloadSHA256 string
	Bytes               int64
	Samples             int64
	Keyframes           int
	Complete            bool
	Recovered           bool
	Faults              []*fault.Error
}

// LayerReport aggregates one representation.
type LayerReport struct {
	Layer               string
	Kind                string
	Playlist            string
	Segments            []SegmentReport
	Isolated            bool
	IsolatedReason      string
	IsolatedOffset      int64
	KeyframeHits        int
	KeyframeTotal       int
	IndexFalsePositives int
	Rebuilt             bool
	Gaps                []*fault.Error
}

// Gap is one detected timeline discontinuity.
type Gap struct {
	Layer          string
	AtSequence     int64
	Declared       string
	Actual         string
	DeviationTicks int64
	Timescale      uint32
}

// Report is the complete, deterministic run report.
type Report struct {
	Case               string
	Root               string
	OutRoot            string
	ParseRoundtrip     int
	ParseTotal         int
	KeyframeHits       int
	KeyframeTotal      int
	SegmentSHA256Match int
	SegmentTotal       int
	Layers             []LayerReport
	Gaps               []Gap
	Faults             []*fault.Error
}

// AddFault records a fault from any stage.
func (r *Report) AddFault(err error) {
	if err == nil {
		return
	}
	if fe, ok := fault.As(err); ok {
		r.Faults = append(r.Faults, fe)
		return
	}
	r.Faults = append(r.Faults, fault.Wrap(fault.KindFormat, "", -1, err.Error(), err))
}

// Summary recomputes aggregate counters from layers.
func (r *Report) Summary() {
	r.KeyframeHits, r.KeyframeTotal = 0, 0
	r.SegmentSHA256Match, r.SegmentTotal = 0, 0
	for _, l := range r.Layers {
		r.KeyframeHits += l.KeyframeHits
		r.KeyframeTotal += l.KeyframeTotal
		if l.Kind != "video" {
			continue
		}
		for _, s := range l.Segments {
			r.SegmentTotal++
			if s.PayloadSHA256 != "" && s.PayloadSHA256 == s.SourcePayloadSHA256 {
				r.SegmentSHA256Match++
			}
		}
	}
}

// ExitCode is 0 with no faults, 2 when every fault is recoverable, else 1.
func (r *Report) ExitCode() int {
	if len(r.Faults) == 0 {
		return 0
	}
	for _, f := range r.Faults {
		if !f.Recoverable {
			return 1
		}
	}
	return 2
}

// Print emits the stable terminal report consumed by acceptance checks.
func (r *Report) Print(w io.Writer) {
	fmt.Fprintf(w, "case=%s root=%s out=%s\n", r.Case, r.Root, r.OutRoot)
	denom := r.ParseTotal
	if denom == 0 {
		denom = 1
	}
	fmt.Fprintf(w, "parse_roundtrip=%.3f\n", float64(r.ParseRoundtrip)/float64(denom))
	fmt.Fprintf(w, "keyframe_hit=%d/%d\n", r.KeyframeHits, r.KeyframeTotal)
	fmt.Fprintf(w, "segment_sha256_match=%d/%d\n", r.SegmentSHA256Match, r.SegmentTotal)
	fmt.Fprintf(w, "continuity_gap=%d\n", len(r.Gaps))
	for _, g := range r.Gaps {
		fmt.Fprintf(w, "gap layer=%s seq=%d declared=%s actual=%s deviation_ticks=%d timescale=%d\n",
			g.Layer, g.AtSequence, g.Declared, g.Actual, g.DeviationTicks, g.Timescale)
	}
	layers := append([]LayerReport(nil), r.Layers...)
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].Layer < layers[j].Layer })
	for _, l := range layers {
		status := "ok"
		if l.Isolated {
			status = fmt.Sprintf("isolated reason=%q offset=%d", l.IsolatedReason, l.IsolatedOffset)
		}
		extra := ""
		if l.Kind == "video" {
			extra = fmt.Sprintf(" keyframe_hit=%d/%d", l.KeyframeHits, l.KeyframeTotal)
			if l.Rebuilt {
				extra += " index=rebuilt"
			}
		}
		fmt.Fprintf(w, "layer=%s kind=%s segments=%d status=%s%s\n",
			l.Layer, l.Kind, len(l.Segments), status, extra)
		for _, s := range l.Segments {
			state := "committed"
			if !s.Complete {
				state = "partial"
			}
			if s.Recovered {
				state = "reused"
			}
			match := "payload_match=true"
			if s.PayloadSHA256 != s.SourcePayloadSHA256 {
				match = "payload_match=false"
			}
			fmt.Fprintf(w, "  segment layer=%s seq=%d path=%s bytes=%d samples=%d keyframes=%d sha256=%s container_sha256=%s %s state=%s\n",
				s.Layer, s.Sequence, s.Path, s.Bytes, s.Samples, s.Keyframes,
				nonEmpty(s.PayloadSHA256), nonEmpty(s.FileSHA256), match, state)
			for _, f := range s.Faults {
				fmt.Fprintf(w, "  segment_fault %s\n", f.Error())
			}
		}
	}
	if len(r.Faults) > 0 {
		var sum fault.Summary
		sum.Faults = append(sum.Faults, r.Faults...)
		fmt.Fprintf(w, "faults=%d\n", len(r.Faults))
		for _, f := range sum.Sorted() {
			fmt.Fprintf(w, "fault %s\n", f.Error())
		}
	}
	fmt.Fprintf(w, "exit_code=%d\n", r.ExitCode())
}

func nonEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// SegmentDigests returns path->container digest for every committed segment,
// used to assert rerun stability.
func (r *Report) SegmentDigests() map[string]string {
	out := map[string]string{}
	for _, l := range r.Layers {
		for _, s := range l.Segments {
			if s.FileSHA256 != "" {
				out[s.Path] = s.FileSHA256
			}
		}
	}
	return out
}

// FaultLines returns the printable fault lines, sorted deterministically.
func (r *Report) FaultLines() []string {
	var sum fault.Summary
	sum.Faults = append(sum.Faults, r.Faults...)
	lines := make([]string, 0, len(r.Faults))
	for _, f := range sum.Sorted() {
		lines = append(lines, f.Error())
	}
	return lines
}

var _ = strings.TrimSpace
