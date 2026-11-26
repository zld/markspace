// Package report renders stable, digestible summaries. A digest is computed
// only from normalized analysis output: it contains no wall-clock time,
// process id, map iteration order or randomness, so two runs over identical
// inputs produce byte-identical reports.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
	"mediasync/internal/timeline"
	"mediasync/internal/validate"
)

// Summary is the normalized, digestable projection of a Result.
type Summary struct {
	Name           string
	Window         clock.Tick
	Sources        []string
	EventCount     int
	EventOrder     []string
	Keyframes      validate.KeyframeStat
	MaxTimeDev     clock.Tick
	MaxByteDev     int64
	Damaged        []issue.ByteRange
	IssueKeys      []string
	IssueCount     int
	Admitted       int
	Recomputed     int
	Unrecoverable  int
	Sealed         int
	PacketCount    int
	RTPWraps       int
	RTPDuplicates  int
	RTPRegressions int
	RTPMissing     uint64
	Categories     []string
	FirstFail      string
	CueMaps        []validate.CueMap
	Intervals      []intervalKey
}

type intervalKey struct {
	From, To int
	DTS, PTS clock.Tick
}

// BuildSnapshot projects a Result into a Summary.
func BuildSnapshot(r *validate.Result) Summary {
	s := Summary{
		Name: r.Name, Window: r.Window, Sources: append([]string(nil), r.Sources...),
		EventCount: len(r.Events), Keyframes: r.Keyframes,
		MaxTimeDev: r.MaxTimeDev, MaxByteDev: r.MaxByteDev,
		Damaged:        append([]issue.ByteRange(nil), r.Damaged...),
		IssueCount:     len(r.Issues),
		Admitted:       r.Admitted,
		Recomputed:     r.Recomputed,
		Unrecoverable:  r.Unrecoverable,
		Sealed:         r.Sealed,
		PacketCount:    r.PacketCount,
		RTPWraps:       r.RTPWraps,
		RTPDuplicates:  r.RTPDuplicates,
		RTPRegressions: r.RTPRegressions,
		RTPMissing:     r.RTPMissing,
		CueMaps:        append([]validate.CueMap(nil), r.CueMaps...),
	}
	sort.Strings(s.Sources)
	for _, ev := range r.Events {
		s.EventOrder = append(s.EventOrder, eventKey(ev))
	}
	for _, it := range r.Issues.Sorted() {
		s.IssueKeys = append(s.IssueKeys, fmt.Sprintf("%s|%s|%d|%d|%d|%s",
			it.Loc.Input, it.Category, it.Loc.Line, it.Loc.ByteOffset, it.Loc.Packet, it.Loc.Ref))
	}
	for _, iv := range r.Intervals {
		s.Intervals = append(s.Intervals, intervalKey{From: iv.From, To: iv.To, DTS: iv.DTSGap, PTS: iv.PTSGap})
	}
	s.Categories = r.Issues.Categories()
	if r.First != nil {
		s.FirstFail = r.First.String()
	}
	return s
}

func eventKey(ev timeline.Event) string {
	return fmt.Sprintf("%s|%s|%d|%d|%d|%d|%d|%s",
		ev.Kind, ev.Input, ev.Time, ev.End, ev.Line, ev.Offset, ev.Packet, ev.Ref)
}

// Digest hashes the summary. The byte stream is length-prefixed so distinct
// field boundaries can never collide.
func Digest(s Summary) string {
	h := sha256.New()
	w := func(parts ...string) {
		for _, p := range parts {
			fmt.Fprintf(h, "%d:%s;", len(p), p)
		}
	}
	w("mediasync-report-v1", s.Name, s.Window.String())
	for _, src := range s.Sources {
		w("src", src)
	}
	w("events", fmt.Sprint(s.EventCount))
	for _, k := range s.EventOrder {
		w("ev", k)
	}
	w("kf", fmt.Sprint(s.Keyframes.Count), s.Keyframes.Min.String(),
		s.Keyframes.Median.String(), s.Keyframes.Max.String())
	w("dev", s.MaxTimeDev.String(), fmt.Sprint(s.MaxByteDev))
	for _, d := range s.Damaged {
		w("dmg", d.Input, fmt.Sprint(d.Start), fmt.Sprint(d.End))
	}
	for _, c := range s.Categories {
		w("cat", c)
	}
	w("rtp", fmt.Sprint(s.PacketCount), fmt.Sprint(s.RTPWraps),
		fmt.Sprint(s.RTPDuplicates), fmt.Sprint(s.RTPRegressions), fmt.Sprint(s.RTPMissing))
	w("issues", fmt.Sprint(s.IssueCount))
	for _, k := range s.IssueKeys {
		w("is", k)
	}
	for _, m := range s.CueMaps {
		w("cue", fmt.Sprint(m.CueIndex), m.CueStart.String(), fmt.Sprint(m.Keyframe),
			m.KeyframePTS.String(), m.TimeDev.String(), fmt.Sprint(m.ByteDev),
			fmt.Sprint(m.Packet), m.PacketDev.String())
	}
	for _, iv := range s.Intervals {
		w("iv", fmt.Sprint(iv.From), fmt.Sprint(iv.To), iv.DTS.String(), iv.PTS.String())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Render produces the terminal report for one result. It is deterministic and
// free of timing data.
func Render(r *validate.Result) string {
	var b strings.Builder
	s := BuildSnapshot(r)
	fmt.Fprintf(&b, "sample: %s\n", r.Name)
	fmt.Fprintf(&b, "  window: %s\n", r.Window)
	fmt.Fprintf(&b, "  inputs: %s\n", strings.Join(s.Sources, ", "))
	fmt.Fprintf(&b, "  events: %d (admitted=%d recomputed=%d unrecoverable=%d sealed=%d)\n",
		s.EventCount, s.Admitted, s.Recomputed, s.Unrecoverable, s.Sealed)
	fmt.Fprintf(&b, "  keyframes: count=%d min=%s median=%s max=%s\n",
		s.Keyframes.Count, s.Keyframes.Min, s.Keyframes.Median, s.Keyframes.Max)
	if s.PacketCount > 0 {
		fmt.Fprintf(&b, "  packets: count=%d wraps=%d duplicates=%d regressions=%d missing=%d\n",
			s.PacketCount, s.RTPWraps, s.RTPDuplicates, s.RTPRegressions, s.RTPMissing)
	}
	renderKeyframeIntervals(&b, r)
	renderCueMaps(&b, r)
	if r.VFR {
		fmt.Fprintf(&b, "  variable frame rate: true (%s); intervals use actual decode time\n", r.VFRReason)
	}
	fmt.Fprintf(&b, "  max time deviation: %s (%d ticks)\n", r.MaxTimeDev, int64(r.MaxTimeDev))
	fmt.Fprintf(&b, "  max byte deviation: %d\n", r.MaxByteDev)
	if r.DriftKnown {
		fmt.Fprintf(&b, "  clock drift: %s\n", r.Drift.Scope())
		if r.Drift.Exceeded {
			fmt.Fprintf(&b, "  clock drift action: report only, original timestamps unchanged\n")
		}
	}
	renderDamage(&b, s)
	fmt.Fprintf(&b, "  replay: %d late event(s) inside window\n", len(r.Replays))
	for _, rp := range r.Replays {
		fmt.Fprintf(&b, "    %s\n", timeline.DescribeReplay(rp))
	}
	fmt.Fprintf(&b, "  errors: %d\n", s.IssueCount)
	if len(s.Categories) > 0 {
		fmt.Fprintf(&b, "  error categories: %s\n", strings.Join(s.Categories, ", "))
	}
	for _, it := range r.Issues.Sorted() {
		recoverable := ""
		if it.Recoverable {
			recoverable = " (recoverable)"
		}
		fmt.Fprintf(&b, "    %s%s\n", it.String(), recoverable)
	}
	if first := firstFailure(r); first != nil {
		fmt.Fprintf(&b, "  first failure: %s\n", first.String())
	} else {
		fmt.Fprintf(&b, "  first failure: none\n")
	}
	if len(s.Damaged) > 0 {
		fmt.Fprintf(&b, "  damaged byte range: %s\n", s.Damaged[0])
	} else {
		fmt.Fprintf(&b, "  damaged byte range: none\n")
	}
	fmt.Fprintf(&b, "  sha256: %s\n", Digest(s))
	return b.String()
}

func renderKeyframeIntervals(b *strings.Builder, r *validate.Result) {
	if len(r.Intervals) == 0 {
		return
	}
	// Show a bounded, deterministic slice so reports stay readable.
	const maxShown = 8
	fmt.Fprintf(b, "  keyframe intervals (actual time):\n")
	for i, iv := range r.Intervals {
		if i >= maxShown {
			fmt.Fprintf(b, "    ... %d more\n", len(r.Intervals)-maxShown)
			break
		}
		fmt.Fprintf(b, "    #%d->#%d dts=%s pts=%s samples=%d\n",
			iv.From, iv.To, iv.DTSGap, iv.PTSGap, iv.Samples)
	}
}

func renderCueMaps(b *strings.Builder, r *validate.Result) {
	if len(r.CueMaps) == 0 {
		return
	}
	fmt.Fprintf(b, "  cue mapping:\n")
	for _, m := range r.CueMaps {
		fmt.Fprintf(b, "    cue %d (line %d) @%s -> keyframe #%d @%s dev=%s",
			m.CueIndex, m.Line, m.CueStart, m.Keyframe, m.KeyframePTS, m.TimeDev)
		if m.Packet > 0 {
			fmt.Fprintf(b, " packet #%d dev=%s", m.Packet, m.PacketDev)
		}
		fmt.Fprintf(b, " bytes=%d\n", m.ByteDev)
	}
}

func renderDamage(b *strings.Builder, s Summary) {
	if len(s.Damaged) == 0 {
		return
	}
	fmt.Fprintf(b, "  damaged ranges:\n")
	for _, d := range s.Damaged {
		fmt.Fprintf(b, "    %s\n", d.String())
	}
}

// firstFailure prefers the explicitly recorded first issue but falls back to
// the deterministic head of the issue list, so callers that construct a
// Result directly still get a stable first-failure line.
func firstFailure(r *validate.Result) *issue.Issue {
	if r.First != nil {
		return r.First
	}
	if it, ok := r.Issues.First(); ok {
		return &it
	}
	return nil
}

// Verdict classifies the overall run.
type Verdict int

const (
	// Pass means no issues and the invariants held.
	Pass Verdict = iota
	// DataErrors means material problems were found and fully reported.
	DataErrors
	// ExecFailure means the command could not run.
	ExecFailure
)

// Compare reports whether two summaries agree on canonical content.
func Compare(a, b Summary) bool { return Digest(a) == Digest(b) }
