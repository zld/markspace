package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"rtp-rebuild/internal/fault"

	"rtp-rebuild/internal/stage"
)

// report is the printable result of one invocation.
type report struct {
	cfg     *Config
	start   time.Time
	streams []*streamReport

	outcome *stage.Outcome

	// Global values filled after the run.
	manifestSHA string
	resumeStage string
	elapsedMS   int64
	budgetDelta int64

	items       []itemJSON
	passed      bool
	interrupted bool
	app         *app
	// hardErr is set when the run failed before validation could complete.
	hardErr error
}

// streamReport is one stream's printed block.
type streamReport struct {
	Name            string
	Packets         int
	Unique          int
	Duplicates      int
	Loss            int
	LossRate        float64
	Jitter          float64
	GapIntervals    int
	OffsetErrors    int
	TruncatedAt     int64
	PeakBuffer      int64
	Intervals       []intervalJSON
	UnalignableList []alignEventJSON
	ClockKnown      bool
	ArtifactSHA     string
	ManifestSHA     string
	ResumeStage     string
	Items           []itemJSON
	ExpectedFault   bool
	ElapsedMS       int64
	BudgetDeltaMS   int64
	PacketsPending  int64
}

func newReport(cfg *Config, start time.Time, works []*work) *report {
	r := &report{cfg: cfg, start: start}
	for _, w := range works {
		r.streams = append(r.streams, &streamReport{Name: w.Name, TruncatedAt: -1})
	}
	return r
}

func (r *report) setOutcome(o *stage.Outcome) {
	r.outcome = o
	if o != nil {
		r.resumeStage = o.ResumeStage
	}
}

// finalizeInterrupted fills the report from the work completed before an
// intentional interruption. No validation items exist yet.
func (r *report) finalizeInterrupted(a *app) {
	r.elapsedMS = time.Since(r.start).Milliseconds()
	r.budgetDelta = r.elapsedMS - r.cfg.BudgetMS
	if r.outcome != nil {
		r.resumeStage = r.outcome.ResumeStage
	}
	r.manifestSHA = a.currentManifestDigest()
	r.interrupted = true
	r.passed = false
	for i, w := range a.work {
		sr := r.streams[i]
		sr.Packets = w.Stream.RTPRecords
		sr.Unique = len(w.Reorder.Packets)
		sr.Duplicates = w.Reorder.Duplicates
		sr.Loss = w.Reorder.LossTotal
		sr.LossRate = w.Reorder.LossRate
		sr.Jitter = w.RTCP.Jitter
		sr.GapIntervals = len(w.Align.Intervals)
		sr.OffsetErrors = w.Reorder.ParseErrors + w.RTCP.Errors
		sr.TruncatedAt = w.RTCP.TruncOffset
		sr.ArtifactSHA = w.Mux.ArtifactSHA
		sr.ManifestSHA = r.manifestSHA
		sr.ResumeStage = r.resumeStage
		sr.ElapsedMS = r.elapsedMS
		sr.BudgetDeltaMS = r.budgetDelta
		sr.PeakBuffer = int64(w.Reorder.PeakPending)
		sr.Intervals = w.Align.Intervals
		sr.ClockKnown = w.Stream.StartUnixNano != 0
	}
}

// finalize fills the report from the completed pipeline state.
func (r *report) finalize(a *app) {
	r.elapsedMS = time.Since(r.start).Milliseconds()
	r.budgetDelta = r.elapsedMS - r.cfg.BudgetMS
	if r.resumeStage == "" {
		r.resumeStage = "complete"
	}
	r.app = a
	r.manifestSHA = a.currentManifestDigest()
	for i, w := range a.work {
		sr := r.streams[i]
		sr.Packets = w.Stream.RTPRecords
		sr.Unique = len(w.Reorder.Packets)
		sr.Duplicates = w.Reorder.Duplicates
		sr.Loss = w.Reorder.LossTotal
		sr.LossRate = w.Reorder.LossRate
		sr.Jitter = w.RTCP.Jitter
		sr.GapIntervals = len(w.Align.Intervals)
		sr.OffsetErrors = w.Reorder.ParseErrors + w.RTCP.Errors
		sr.TruncatedAt = w.RTCP.TruncOffset
		if sr.TruncatedAt < 0 && w.Reorder.FirstErrorOff >= 0 {
			sr.TruncatedAt = w.Reorder.FirstErrorOff
		}
		sr.ArtifactSHA = w.Mux.ArtifactSHA
		sr.ManifestSHA = r.manifestSHA
		sr.ResumeStage = r.resumeStage
		sr.ElapsedMS = r.elapsedMS
		sr.BudgetDeltaMS = r.budgetDelta
		sr.Items = append(sr.Items, w.Validate.Items...)
		sr.Items = append(sr.Items, a.thresholdItems(w)...)
		sr.PeakBuffer = int64(w.Reorder.PeakPending)
		sr.Intervals = w.Align.Intervals
		sr.ClockKnown = w.Stream.StartUnixNano != 0
		for _, e := range w.Align.Events {
			if !e.Alignable {
				sr.UnalignableList = append(sr.UnalignableList, e)
			}
		}
	}
	r.passed = r.computeVerdict(a)
	r.items = r.allItems()
}

// thresholdItems adds the business-threshold items for a stream.
func (a *app) thresholdItems(w *work) []itemJSON {
	return []itemJSON{
		{Name: "loss_rate_threshold", Pass: w.Reorder.LossRate <= 0.02,
			Detail: fmt.Sprintf("loss_rate=%.4f max=0.0200", w.Reorder.LossRate)},
		{Name: "gap_threshold", Pass: len(w.Align.Intervals) == 0,
			Detail: fmt.Sprintf("gap_intervals=%d", len(w.Align.Intervals))},
		{Name: "structural_offsets", Pass: w.Reorder.ParseErrors+w.RTCP.Errors == 0,
			Detail: fmt.Sprintf("offset_errors=%d", w.Reorder.ParseErrors+w.RTCP.Errors)},
	}
}

// allItems flattens the per-stream items for the summary line.
func (r *report) allItems() []itemJSON {
	var out []itemJSON
	for _, s := range r.streams {
		for _, it := range s.Items {
			c := it
			c.Name = s.Name + "." + it.Name
			out = append(out, c)
		}
	}
	return out
}

// computeVerdict decides pass/fail. In self-test mode the fault stream is
// expected to be damaged: it passes when the expected damage is reported and
// every other item still holds. The healthy stream must pass every item.
func (r *report) computeVerdict(a *app) bool {
	ok := true
	for i, s := range r.streams {
		faulty := a.work[i].Stream.TruncatedOffset >= 0 || strings.Contains(s.Name, "fault")
		for _, it := range s.Items {
			if it.Pass {
				continue
			}
			if faulty && isExpectedFaultItem(it) {
				continue
			}
			ok = false
		}
		if faulty && r.cfg.SelfTest {
			// The fault stream must actually exhibit the damage it claims, and
			// the truncation offset must be a real position in the input.
			if s.GapIntervals < 1 || s.OffsetErrors < 1 || s.TruncatedAt < 0 {
				ok = false
			}
		}
	}
	return ok
}

func isExpectedFaultItem(it itemJSON) bool {
	switch it.Name {
	case "gap_threshold", "structural_offsets", "loss_rate_threshold", "loss_rate", "gap_intervals":
		return true
	}
	return false
}

// exitCode maps the report to the documented exit codes.
//
// Precedence: a completed run that carries locatable corruption (a reported
// input offset) exits 3, because a partial but usable trajectory was produced
// and every damaged item can be pointed at. A run whose artifacts were
// withheld or whose thresholds failed without a locatable cause exits 1.
func (r *report) exitCode() int {
	if r.hardErr != nil {
		return exitParam
	}
	if r.interrupted {
		// An intentional interruption is not a threshold failure.
		return exitOK
	}
	if a := r.app; a != nil && (a.repeatedManifestMismatch || a.unpublished) {
		return exitFail
	}
	if r.passed {
		return exitOK
	}
	locatable := false
	for _, s := range r.streams {
		if s.OffsetErrors > 0 && s.ArtifactSHA != "" {
			locatable = true
		}
	}
	if locatable {
		return exitPartial
	}
	return exitFail
}

// classify turns a hard error into an exit code.
//
// A time-budget overrun is a business threshold failure (exit 1); a parameter
// or IO problem is unrecoverable (exit 2).
func classify(err error, rep *report) int {
	if err == nil {
		if rep != nil && rep.passed {
			return exitOK
		}
		return exitFail
	}
	if fault.Is(err, fault.ErrBudget) {
		return exitFail
	}
	return exitParam
}

// print writes the human-readable report.
func (r *report) print(w io.Writer) {
	for _, s := range r.streams {
		fmt.Fprintf(w, "stream=%s packets=%d unique=%d duplicates=%d loss=%d loss_rate=%.4f\n",
			s.Name, s.Packets, s.Unique, s.Duplicates, s.Loss, s.LossRate)
		fmt.Fprintf(w, "stream=%s jitter=%.2f gap_intervals=%d offset_errors=%d truncated_offset=%d\n",
			s.Name, s.Jitter, s.GapIntervals, s.OffsetErrors, s.TruncatedAt)
		fmt.Fprintf(w, "stream=%s artifact_sha256=%s\n", s.Name, orDash(s.ArtifactSHA))
	}
	fmt.Fprintf(w, "peak_buffer_bytes=%d\n", r.peakBuffer())
	fmt.Fprintf(w, "elapsed_ms=%d budget_ms=%d budget_delta_ms=%d\n",
		r.elapsedMS, r.cfg.BudgetMS, r.budgetDelta)
	fmt.Fprintf(w, "manifest_sha256=%s\n", orDash(r.manifestSHA))
	fmt.Fprintf(w, "resume_stage=%s\n", orDash(r.resumeStage))
	fmt.Fprintf(w, "clock_offset=%s\n", r.clockOffsetLabel())
	fmt.Fprintf(w, "unalignable_events=%d\n", r.unalignableEvents())

	for _, s := range r.streams {
		if len(s.Intervals) > 0 {
			for _, iv := range s.Intervals {
				fmt.Fprintf(w, "interval stream=%s start_seq=%d end_seq=%d start_ms=%d end_ms=%d gap_ticks=%d events=%d\n",
					s.Name, iv.StartSeq, iv.EndSeq, int64(iv.StartTime)/90, int64(iv.EndTime)/90,
					iv.Gap, len(iv.Events))
			}
			continue
		}
	}
	for _, s := range r.streams {
		for _, e := range s.UnalignableList {
			fmt.Fprintf(w, "unalignable stream=%s line=%d kind=%s reason=%s\n", s.Name, e.Line, e.Kind, e.Reason)
		}
	}

	items := append([]itemJSON(nil), r.items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	for _, it := range items {
		verdict := "PASS"
		if !it.Pass {
			verdict = "FAIL"
		}
		fmt.Fprintf(w, "item %s %s %s\n", it.Name, verdict, it.Detail)
	}
	if r.app != nil && r.app.unpublished {
		fmt.Fprintln(w, "artifacts=withheld")
	}
	switch {
	case r.interrupted:
		fmt.Fprintln(w, "result=INTERRUPTED")
	case r.passed:
		fmt.Fprintln(w, "result=PASS")
	default:
		fmt.Fprintln(w, "result=FAIL")
	}
	if r.outcome != nil && r.outcome.Interrupted != "" {
		fmt.Fprintf(w, "interrupted_after=%s\n", r.outcome.Interrupted)
	}
}

func (r *report) peakBuffer() int64 {
	var peak int64
	for _, s := range r.streams {
		if s.PeakBuffer > peak {
			peak = s.PeakBuffer
		}
	}
	return peak
}

// unalignableEvents counts events that could not be placed on the timeline.
func (r *report) unalignableEvents() int {
	n := 0
	for _, s := range r.streams {
		n += len(s.UnalignableList)
	}
	return n
}

// clockOffsetLabel states whether the media clock could be tied to wall time.
func (r *report) clockOffsetLabel() string {
	for _, s := range r.streams {
		if !s.ClockKnown {
			return "unknown"
		}
	}
	return "known"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// currentManifestDigest hashes the manifest currently on disk, falling back to
// the in-memory manifest before it is published.
func (a *app) currentManifestDigest() string {
	if data, err := os.ReadFile(a.ws.manifestPath()); err == nil {
		return digest(data)
	}
	data, err := a.mf.Bytes()
	if err != nil {
		return ""
	}
	return digest(data)
}
