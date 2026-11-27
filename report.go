package mediacore

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// ItemStatus is the per-fragment verdict rendered in the report.
type ItemStatus struct {
	Asset   string
	Version string
	TrackID uint32
	Seq     uint32
	Status  string
	Stage   string
	Offset  int64
	Detail  string
}

// Report is the complete, deterministic conclusion of one processing run.
// Field names mirror the keys required by the acceptance output.
type Report struct {
	Assets       []string            `json:"assets"`
	Missing      map[uint32][]uint32 `json:"missing"`
	Conflicts    []Conflict          `json:"conflicts"`
	Rollback     []RollbackRecord    `json:"rollback"`
	BudgetMS     int64               `json:"budget_ms"`
	BudgetActual int64               `json:"budget_actual_ms"`
	BudgetDelta  int64               `json:"budget_delta_ms"`
	BudgetExceed bool                `json:"budget_exceeded"`
	AnomalyStart Nano                `json:"anomaly_start_ns"`
	AnomalyEnd   Nano                `json:"anomaly_end_ns"`
	Processed    int                 `json:"processed"`
	Rejected     int                 `json:"rejected"`
	Committed    []Key               `json:"committed"`
	Items        []ItemStatus        `json:"items"`
	ColorDropped []ColorCandidate    `json:"color_dropped"`
}

// ReportInput bundles the artifacts produced by the pipeline.
type ReportInput struct {
	Assets    []string
	Items     []*Item
	Missing   map[uint32][]uint32
	Conflicts []Conflict
	Rollbacks []RollbackRecord
	Budget    *Budget
	Anomalies []RTPAnomaly
	Committed []Key
	ColorRes  []ColorResolution
}

// BuildReport assembles a report without touching I/O.
func BuildReport(in ReportInput) *Report {
	r := &Report{Assets: append([]string(nil), in.Assets...),
		Missing: map[uint32][]uint32{}, Conflicts: in.Conflicts,
		Rollback: in.Rollbacks, Committed: append([]Key(nil), in.Committed...)}
	for track, seqs := range in.Missing {
		r.Missing[track] = append([]uint32(nil), seqs...)
	}
	if in.Budget != nil {
		r.BudgetMS = in.Budget.TimeBudgetMS
		r.BudgetActual = in.Budget.ActualElapsedMS
		r.BudgetDelta = in.Budget.ActualElapsedMS - in.Budget.TimeBudgetMS
		r.BudgetExceed = in.Budget.Exceeded ||
			(in.Budget.TimeBudgetMS > 0 && in.Budget.ActualElapsedMS > in.Budget.TimeBudgetMS)
	}
	for _, a := range in.Anomalies {
		if r.AnomalyEnd == 0 || a.EndNS > r.AnomalyEnd {
			r.AnomalyEnd = a.EndNS
		}
		if r.AnomalyStart == 0 || a.StartNS < r.AnomalyStart {
			r.AnomalyStart = a.StartNS
		}
	}
	for _, it := range in.Items {
		st := ItemStatus{Asset: it.Asset, Version: it.Version, TrackID: it.TrackID,
			Seq: it.Seq, Status: it.Status, Offset: it.Ref.ByteOff}
		if it.Reason != nil {
			st.Stage = it.Reason.Stage
			st.Detail = it.Reason.Error()
		}
		r.Items = append(r.Items, st)
		if it.Status == "processed" {
			r.Processed++
		}
		if it.Status == "rejected" {
			r.Rejected++
		}
	}
	for _, cr := range in.ColorRes {
		r.ColorDropped = append(r.ColorDropped, cr.Dropped...)
	}
	sort.Strings(r.Assets)
	return r
}

// Render writes the stable, greppable key/value acceptance view to w.
func (r *Report) Render(w io.Writer) {
	fmt.Fprintf(w, "assets: %s\n", strings.Join(r.Assets, ","))
	fmt.Fprintf(w, "missing: %s\n", formatMissing(r.Missing))
	fmt.Fprintf(w, "conflicts: %d%s\n", len(r.Conflicts), formatConflict(r.Conflicts))
	fmt.Fprintf(w, "rollback: %d%s\n", len(r.Rollback), formatRollback(r.Rollback))
	fmt.Fprintf(w, "budget_ms: budget=%d actual=%d delta=%d exceeded=%t\n",
		r.BudgetMS, r.BudgetActual, r.BudgetDelta, r.BudgetExceed)
	fmt.Fprintf(w, "anomaly_start_ns: %d\n", int64(r.AnomalyStart))
	fmt.Fprintf(w, "anomaly_end_ns: %d\n", int64(r.AnomalyEnd))
	fmt.Fprintf(w, "processed: %d\n", r.Processed)
	fmt.Fprintf(w, "rejected: %d\n", r.Rejected)
	fmt.Fprintf(w, "committed: %s\n", formatKeys(r.Committed))
	for _, it := range r.Items {
		fmt.Fprintf(w, "item: asset=%s version=%s track=%d seq=%d status=%s",
			it.Asset, it.Version, it.TrackID, it.Seq, it.Status)
		if it.Stage != "" {
			fmt.Fprintf(w, " stage=%s offset=%d", it.Stage, it.Offset)
		}
		if it.Detail != "" {
			fmt.Fprintf(w, " detail=%q", it.Detail)
		}
		fmt.Fprintln(w)
	}
	for _, d := range r.ColorDropped {
		fmt.Fprintf(w, "color_dropped: asset=%s version=%s prio=%d\n", d.Asset, d.Version, d.Prio)
	}
}

func formatMissing(m map[uint32][]uint32) string {
	tracks := make([]uint32, 0, len(m))
	for t := range m {
		tracks = append(tracks, t)
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i] < tracks[j] })
	var parts []string
	for _, t := range tracks {
		seqs := m[t]
		s := make([]string, len(seqs))
		for i, v := range seqs {
			s[i] = utoa(v)
		}
		parts = append(parts, "track"+utoa(t)+"=["+strings.Join(s, ",")+"]")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ";")
}

func formatConflict(cs []Conflict) string {
	if len(cs) == 0 {
		return ""
	}
	var parts []string
	for _, c := range cs {
		vs := make([]string, len(c.Versions))
		for i, v := range c.Versions {
			vs[i] = v.Asset + "/" + v.Version
		}
		parts = append(parts, fmt.Sprintf(" track%d-seq%d=[%s]", c.Key.TrackID, c.Key.Seq, strings.Join(vs, ",")))
	}
	return " " + strings.Join(parts, " ")
}

func formatRollback(rs []RollbackRecord) string {
	if len(rs) == 0 {
		return ""
	}
	var parts []string
	for _, rec := range rs {
		parts = append(parts, fmt.Sprintf(" track%d-seq%d@%s:%d", rec.TrackID, rec.Seq, rec.Stage, rec.Offset))
	}
	return " " + strings.Join(parts, " ")
}

func formatKeys(ks []Key) string {
	if len(ks) == 0 {
		return "none"
	}
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = "track" + utoa(k.TrackID) + "-seq" + utoa(k.Seq)
	}
	return strings.Join(parts, ",")
}
