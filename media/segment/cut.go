// Package segment is the slicing subsystem. It expands event intervals on
// the shared rational clock, locates keyframes independently per bitrate
// layer, extracts byte-bounded fMP4 fragment ranges without re-encoding, and
// cross-checks layer boundaries on the shared clock. Corrupt source ranges
// stop at the last valid fragment or jump to the next intact keyframe and
// record exactly what media interval is left uncovered.
package segment

import (
	"fmt"

	"replaykit/media/audit"
	"replaykit/media/clock"
	"replaykit/media/fault"
	gomp4mp4 "replaykit/media/mp4"
)

// Event is one highlight trigger. Times are absolute event-nanoseconds and
// Pre/Post extend the interval before and after the trigger.
type Event struct {
	ID     string `json:"id"`
	AtNS   int64  `json:"at_ns"`
	PreNS  int64  `json:"pre_ns"`
	PostNS int64  `json:"post_ns"`
}

// ByteRange is an inclusive-start, exclusive-end input byte span.
type ByteRange struct {
	Fragment int   `json:"fragment"`
	Start    int64 `json:"start"`
	End      int64 `json:"end"`
	HasMdat  bool  `json:"has_mdat"`
}

// Layer binds one bitrate rendition's bytes to its scan, index and clock.
type Layer struct {
	Name        string
	Data        []byte
	Scan        *gomp4mp4.Scan
	Index       *gomp4mp4.Index
	Clock       *clock.Clock
	InputDigest string
	// CorruptFragments optionally marks 1-based fragment numbers known bad
	// from an external integrity check. Parser-detected bad fragments are
	// already inferred from the rebuilt index.
	CorruptFragments map[int]bool
}

// Options bounds extraction and tunes recovery behaviour.
type Options struct {
	MaxSegmentBytes int64
}

// DefaultSegmentLimit caps one extracted segment at 16 MiB.
const DefaultSegmentLimit int64 = 16 << 20

func (o Options) withDefaults() Options {
	if o.MaxSegmentBytes <= 0 {
		o.MaxSegmentBytes = DefaultSegmentLimit
	}
	return o
}

// Window is one event cut from one layer.
type Window struct {
	Event          Event
	LayerName      string
	InputDigest    string
	StartNS        int64
	EndNS          int64
	AtTick         clock.Tick
	StartTick      clock.Tick
	EndTick        clock.Tick
	LookupHit      bool
	Keyframe       gomp4mp4.Sample
	HasKeyframe    bool
	StartFragment  int
	EndFragment    int
	Ranges         []ByteRange
	Bytes          []byte
	Digest         string
	SampleCount    int
	CoveredEndTick int64
	SkewRefTicks   int64
	Partial        bool
	Recoverable    bool
	Uncovered      *fault.Range
	Anomalies      []fault.Anomaly
}

// FileName returns the deterministic artifact name for this window.
func (w *Window) FileName() string {
	return fmt.Sprintf("%s__%s.fmp4", safeName(w.Event.ID), safeName(w.LayerName))
}

func safeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "x"
	}
	return string(out)
}

type fragmentRegion struct {
	moof  gomp4mp4.BoxRef
	mdat  *gomp4mp4.BoxRef
	init  bool
}

// FragmentRegions pairs every top-level moof with its adjacent mdat and
// returns the init-prefix end (end of moov) plus 1-based fragment regions.
// A moof whose mdat box is missing/untrusted is returned with HasMdat=false.
func FragmentRegions(sc *gomp4mp4.Scan) (initEnd int64, regions map[int]ByteRange) {
	regions = make(map[int]ByteRange)
	tops := sc.TopLevel
	for _, b := range tops {
		if b.Type == "moov" {
			initEnd = b.End()
		}
	}
	fragNo := 0
	for i, b := range tops {
		if b.Type != "moof" {
			continue
		}
		fragNo++
		r := ByteRange{Fragment: fragNo, Start: b.Offset, End: b.End()}
		if i+1 < len(tops) && tops[i+1].Type == "mdat" && tops[i+1].Offset == b.End() {
			r.End = tops[i+1].End()
			r.HasMdat = true
		}
		regions[fragNo] = r
	}
	return initEnd, regions
}

// goodFragments returns fragment numbers that both produced index samples and
// have a complete moof+mdat byte region, minus externally flagged ones.
func goodFragments(l *Layer, regions map[int]ByteRange) map[int]bool {
	hasSamples := make(map[int]bool)
	for _, s := range l.Index.Track.Samples {
		hasSamples[s.Fragment] = true
	}
	good := make(map[int]bool)
	for f, r := range regions {
		if r.HasMdat && hasSamples[f] && !l.CorruptFragments[f] {
			good[f] = true
		}
	}
	return good
}

// PlanWindow maps the event interval, chooses the keyframe and fragment span,
// validates byte bounds and extracts the clip bytes. It never returns a fatal
// error for corrupt media; such conditions are represented on the Window. A
// returned error is reserved for a missing clock/index (no time mapping).
func PlanWindow(ev Event, l *Layer, opts Options) (*Window, error) {
	opts = opts.withDefaults()
	if l == nil || l.Index == nil || l.Clock == nil {
		return nil, fault.Fatal("cut_window", layerName(l), 0, fault.Range{},
			fmt.Errorf("layer missing index/clock: no time mapping"))
	}
	w := &Window{Event: ev, LayerName: l.Name, InputDigest: l.InputDigest, Recoverable: false}

	startNS := ev.AtNS - ev.PreNS
	if startNS < 0 || ev.PreNS > ev.AtNS {
		startNS = 0
	}
	endNS := ev.AtNS + ev.PostNS
	st, err := l.Clock.FromNS(startNS)
	if err != nil {
		return nil, fault.Fatal("cut_window", l.Name, 0, fault.Range{}, err)
	}
	et, err := l.Clock.FromNS(endNS)
	if err != nil {
		return nil, fault.Fatal("cut_window", l.Name, 0, fault.Range{}, err)
	}
	at, err := l.Clock.FromNS(ev.AtNS)
	if err != nil {
		return nil, fault.Fatal("cut_window", l.Name, 0, fault.Range{}, err)
	}
	w.StartNS, w.EndNS, w.StartTick, w.EndTick, w.AtTick = startNS, endNS, st, et, at

	initEnd, regions := FragmentRegions(l.Scan)
	good := goodFragments(l, regions)

	// fragEnd is the exclusive media end of each fragment's valid samples.
	fragEnd := make(map[int]int64)
	for _, s := range l.Index.Track.Samples {
		if s.End() > fragEnd[s.Fragment] {
			fragEnd[s.Fragment] = s.End()
		}
	}

	k, ok := l.Index.KeyframeAtOrBefore(st.Value)
	w.LookupHit = ok
	if !ok {
		w.Anomalies = append(w.Anomalies, fault.New(l.Name, 0, fault.CategoryUncoveredRange,
			fault.SeverityError, fault.ActionNone, l.Index.Track.Timescale,
			fault.Range{Start: st, End: et}, "no keyframe at or before interval start"))
		return w, nil
	}

	// If the chosen keyframe fragment is corrupt, jump forward to the next
	// intact keyframe and leave the skipped media range explicitly uncovered.
	if !good[k.Fragment] {
		w.Anomalies = append(w.Anomalies, fault.New(l.Name, keyOffset(regions, k.Fragment),
			fault.CategorySourceCorrupt, fault.SeverityError, fault.ActionResynced,
			l.Index.Track.Timescale, fault.Range{Start: clock.Tick{Value: k.DecodeTime}, End: st},
			fmt.Sprintf("keyframe fragment %d corrupt; jumping to next valid keyframe", k.Fragment)))
		nk, nok := nextGoodKeyframe(l, k.DecodeTime, good)
		if !nok {
			u := fault.Range{Start: clock.Tick{Value: k.DecodeTime}, End: et}
			w.Uncovered = &u
			w.Anomalies = append(w.Anomalies, fault.New(l.Name, keyOffset(regions, k.Fragment),
				fault.CategoryUncoveredRange, fault.SeverityFatal, fault.ActionNone,
				l.Index.Track.Timescale, u, "no later valid keyframe"))
			return w, nil
		}
		u := fault.Range{Start: clock.Tick{Value: st.Value}, End: clock.Tick{Value: nk.DecodeTime}}
		w.Uncovered = &u
		w.Anomalies = append(w.Anomalies, fault.New(l.Name, 0, fault.CategoryUncoveredRange,
			fault.SeverityWarning, fault.ActionResynced, l.Index.Track.Timescale, u,
			fmt.Sprintf("range recovered from keyframe at %d", nk.DecodeTime)))
		k = nk
		w.Partial = true
	}
	w.Keyframe, w.HasKeyframe = k, true
	w.StartFragment = k.Fragment

	// Determine the nominal ending fragment from overlapping samples.
	endFrag := k.Fragment
	if overlap := l.Index.SamplesInRange(k.DecodeTime, et.Value); len(overlap) > 0 {
		for _, s := range overlap {
			if s.Fragment > endFrag {
				endFrag = s.Fragment
			}
		}
	}

	// Stop at the first non-good fragment in the selected span so a damaged
	// fragment is never partially trusted or copied.
	lastGood := k.Fragment
	for f := k.Fragment; f <= endFrag; f++ {
		if good[f] {
			lastGood = f
			continue
		}
		r := regions[f]
		uncoveredStart := fragEnd[f-1]
		if uncoveredStart == 0 {
			uncoveredStart = k.DecodeTime
		}
		u := fault.Range{Start: clock.Tick{Value: uncoveredStart}, End: et}
		w.Uncovered, w.Partial = &u, true
		w.Anomalies = append(w.Anomalies, fault.New(l.Name, r.Start, fault.CategorySourceCorrupt,
			fault.SeverityError, fault.ActionSkipped, l.Index.Track.Timescale, u,
			fmt.Sprintf("fragment %d damaged/incomplete; stopping at last valid fragment", f)))
		break
	}
	w.EndFragment = lastGood

	// Covered end = end time of the last sample in the retained fragments.
	coveredEnd := fragEnd[lastGood]
	for _, s := range l.Index.Track.Samples {
		if s.Fragment >= k.Fragment && s.Fragment <= lastGood {
			w.SampleCount++
		}
	}
	w.CoveredEndTick = coveredEnd

	// If the requested end runs past the last retained media, record the
	// uncovered tail even though the highlight itself may still be covered.
	if coveredEnd > 0 && coveredEnd < et.Value {
		tail := fault.Range{Start: clock.Tick{Value: coveredEnd}, End: et}
		if w.Uncovered == nil {
			w.Uncovered = &tail
		}
		w.Partial = true
		w.Anomalies = append(w.Anomalies, fault.New(l.Name, keyOffset(regions, lastGood+1),
			fault.CategoryUncoveredRange, fault.SeverityWarning, fault.ActionSkipped,
			l.Index.Track.Timescale, tail, "requested interval extends beyond valid media"))
	}

	// The highlight instant itself must be covered for a recoverable cut.
	// coveredEnd is exclusive, so at == coveredEnd is still addressable by
	// the preceding sample and counts as covered.
	if coveredEnd < at.Value {
		u := fault.Range{Start: clock.Tick{Value: coveredEnd}, End: et}
		if w.Uncovered == nil {
			w.Uncovered = &u
		}
		w.Anomalies = append(w.Anomalies, fault.New(l.Name, 0, fault.CategoryUncoveredRange,
			fault.SeverityFatal, fault.ActionNone, l.Index.Track.Timescale, u,
			fmt.Sprintf("event at tick %d lies beyond covered end %d", at.Value, coveredEnd)))
		return w, nil
	}

	// Assemble init + retained fragment byte ranges.
	if initEnd <= 0 || initEnd > int64(len(l.Data)) {
		return nil, fault.Fatal("cut_window", l.Name, 0, fault.Range{}, fmt.Errorf("moov/init range invalid"))
	}
	if int64(initEnd) > opts.MaxSegmentBytes {
		return nil, fmt.Errorf("segment init %d exceeds byte limit %d", initEnd, opts.MaxSegmentBytes)
	}
	buf := append([]byte{}, l.Data[:initEnd]...)
	for f := k.Fragment; f <= lastGood; f++ {
		r := regions[f]
		if r.End > int64(len(l.Data)) || r.Start < 0 || r.Start >= r.End {
			return nil, fault.Fatal("cut_window", l.Name, r.Start, fault.Range{},
				fmt.Errorf("fragment %d range [%d,%d) outside %d bytes", f, r.Start, r.End, len(l.Data)))
		}
		if int64(len(buf))+(r.End-r.Start) > opts.MaxSegmentBytes {
			return nil, fmt.Errorf("segment exceeds max bytes %d", opts.MaxSegmentBytes)
		}
		buf = append(buf, l.Data[r.Start:r.End]...)
		w.Ranges = append(w.Ranges, r)
	}
	w.Bytes = buf
	w.Digest = audit.HashBytes(buf)
	w.Recoverable = true
	return w, nil
}

func keyOffset(regions map[int]ByteRange, frag int) int64 {
	if r, ok := regions[frag]; ok {
		return r.Start
	}
	return 0
}

func layerName(l *Layer) string {
	if l == nil {
		return ""
	}
	return l.Name
}

func nextGoodKeyframe(l *Layer, afterDecode int64, good map[int]bool) (gomp4mp4.Sample, bool) {
	var best gomp4mp4.Sample
	found := false
	for _, s := range l.Index.Track.Samples {
		if !s.Key || s.DecodeTime <= afterDecode || !good[s.Fragment] {
			continue
		}
		if !found || s.DecodeTime < best.DecodeTime {
			best, found = s, true
		}
	}
	return best, found
}

// Record maps the window onto an audit segment record.
func (w *Window) Record() audit.SegmentRecord {
	state := audit.StateBuilt
	switch {
	case !w.Recoverable:
		state = audit.StateFailed
	case w.Partial:
		state = audit.StatePartial
	}
	rec := audit.SegmentRecord{
		Key: fmt.Sprintf("%s/%s", w.Event.ID, w.LayerName), EventID: w.Event.ID, Layer: w.LayerName,
		Path: w.FileName(), InputDigest: w.InputDigest,
		RangeStartNS: w.StartNS, RangeEndNS: w.EndNS,
		MediaStart: w.Keyframe.DecodeTime, MediaEnd: w.CoveredEndTick,
		Keyframe: w.Keyframe.DecodeTime, SampleCount: w.SampleCount, Bytes: len(w.Bytes), Hash: w.Digest,
		HashMatch: w.Recoverable, BoundarySkew: w.SkewRefTicks,
		Partial: w.Partial, State: state,
	}
	if w.Uncovered != nil {
		rec.UncoveredFrom = w.Uncovered.Start.Value
		rec.UncoveredTo = w.Uncovered.End.Value
	}
	if !w.HasKeyframe {
		rec.Keyframe = 0
	}
	return rec
}
