// Package timeline maintains the committed event set of a synthetic
// multi-track archive. A single watermark bounds which events are final:
// watermark = highest event time seen - allowed lateness. Events at or below
// the watermark are committed; later ones wait. Events that arrive after their
// time has already been committed trigger a rollback-replay from the nearest
// checkpoint (see replay.go).
package timeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"archive.example/mediakernel/internal/clock"
	"archive.example/mediakernel/internal/events"
)

// Config bounds the timeline. Every limit produces an explicit archive error
// when exceeded.
type Config struct {
	LateWindowNS    int64 // allowed lateness behind the newest event time
	CheckpointEvery int   // committed events between checkpoints; default 16
	MaxCheckpoints  int   // checkpoints retained; default 16
	MaxPending      int   // uncommitted events retained; default 4096
	MaxEvents       int   // total accepted events; default 20000
	MaxReplayEvents int   // committed events rewound on one replay; default 400
}

func (c Config) withDefaults() Config {
	if c.CheckpointEvery <= 0 {
		c.CheckpointEvery = 16
	}
	if c.MaxCheckpoints <= 0 {
		c.MaxCheckpoints = 16
	}
	if c.MaxPending <= 0 {
		c.MaxPending = 4096
	}
	if c.MaxEvents <= 0 {
		c.MaxEvents = 20000
	}
	if c.MaxReplayEvents <= 0 {
		c.MaxReplayEvents = 400
	}
	return c
}

// Stats is diagnostic bookkeeping, excluded from the state digest.
type Stats struct {
	Admitted   int
	Committed  int
	Pending    int
	Late       int
	Replays    int
	Duplicates int
	Gaps       int
	Rejected   int
	Watermark  int64
	MaxEventAt int64
}

// AdmitResult reports what one admission or gap changed.
type AdmitResult struct {
	Committed  []events.Event // newly committed events, canonical order
	Replayed   []events.Event // committed events whose position changed in a replay
	Late       bool           // the event arrived behind the watermark
	Replay     bool           // a checkpoint rollback happened
	Duplicate  bool           // the identity was already accepted
	FilledGaps int
	Watermark  int64
}

// CommitResult reports events that became final due to a watermark advance.
type CommitResult struct {
	Committed []events.Event
	Watermark int64
}

// Timeline is the authoritative event state. The accepted log is kept in
// canonical order; the committed set is its prefix whose event time lies at or
// below the watermark.
type Timeline struct {
	cfg Config

	accepted        []events.Event
	committedPrefix int
	digests         map[events.Identity][sha256.Size]byte
	gaps            map[gapKey]events.Gap

	maxAt     int64
	hasAt     bool
	watermark int64

	checkpoints []checkpoint
	stats       Stats
}

type gapKey struct {
	track events.TrackID
	kind  events.Kind
	seq   uint64
}

// New constructs a timeline.
func New(cfg Config) (*Timeline, *events.Error) {
	cfg = cfg.withDefaults()
	if cfg.LateWindowNS < 0 {
		return nil, events.NewError(events.CodeInternal, "", 0, -1, "timeline", "negative late window", nil)
	}
	if cfg.LateWindowNS > clock.MaxTimelineNS {
		return nil, events.NewError(events.CodeInternal, "", 0, -1, "timeline",
			"late window exceeds timeline length", nil)
	}
	return &Timeline{
		cfg:     cfg,
		digests: make(map[events.Identity][sha256.Size]byte, 64),
		gaps:    make(map[gapKey]events.Gap, 64),
	}, nil
}

// Config returns the effective configuration.
func (t *Timeline) Config() Config { return t.cfg }

// Watermark returns the current commit boundary in nanoseconds.
func (t *Timeline) Watermark() int64 { return t.watermark }

// MaxEventAt returns the highest event time admitted so far.
func (t *Timeline) MaxEventAt() int64 { return t.maxAt }

// Stats returns a diagnostic snapshot.
func (t *Timeline) Stats() Stats { return t.stats }

func validateEvent(e events.Event) *events.Error {
	if e.Track == "" {
		return events.NewError(events.CodeMalformed, e.Track, e.Seq, -1, "admit", "event has no track", nil)
	}
	if e.Kind == "" {
		return events.NewError(events.CodeMalformed, e.Track, e.Seq, -1, "admit", "event has no kind", nil)
	}
	if e.At < -clock.MaxTimelineNS || e.At > clock.MaxTimelineNS {
		return events.NewError(events.CodeTimeOutOfRange, e.Track, e.Seq, -1, "admit",
			fmt.Sprintf("event time %d outside timeline", e.At), nil)
	}
	if e.Duration < 0 || e.Duration > clock.MaxTimelineNS {
		return events.NewError(events.CodeTimeOutOfRange, e.Track, e.Seq, -1, "admit",
			fmt.Sprintf("duration %d outside timeline", e.Duration), nil)
	}
	return nil
}

// Admit inserts an event and advances the watermark, committing whatever the
// new watermark makes final.
func (t *Timeline) Admit(e events.Event) (AdmitResult, *events.Error) {
	if err := validateEvent(e); err != nil {
		return AdmitResult{}, err
	}
	if len(t.accepted) >= t.cfg.MaxEvents {
		return AdmitResult{}, events.NewError(events.CodeCapacity, e.Track, e.Seq, -1, "admit",
			fmt.Sprintf("event limit %d reached", t.cfg.MaxEvents), nil)
	}
	dg := canonicalDigest(e)
	if prev, ok := t.digests[e.Identity()]; ok {
		t.stats.Duplicates++
		if prev != dg {
			return AdmitResult{}, events.NewError(events.CodeMalformed, e.Track, e.Seq, -1, "admit",
				"conflicting duplicate payload for same identity", nil)
		}
		return AdmitResult{Duplicate: true, Watermark: t.watermark}, nil
	}

	index, _ := t.find(e)
	late := index < t.committedPrefix
	var res AdmitResult
	var err *events.Error
	if late {
		res, err = t.replayLate(e, index)
	} else {
		t.insertAt(index, e)
		res = AdmitResult{}
	}
	if err != nil {
		return AdmitResult{}, err
	}

	if e.At > t.maxAt || !t.hasAt {
		t.maxAt = e.At
		t.hasAt = true
	}
	t.advanceStats()
	t.watermark = t.maxAt - t.cfg.LateWindowNS
	if t.watermark < 0 {
		// Negative watermarks are legal for pre-roll events but the commit
		// boundary itself is bounded by the timeline.
		if t.watermark < -clock.MaxTimelineNS {
			t.watermark = -clock.MaxTimelineNS
		}
	}

	committed, cerr := t.commit()
	if cerr != nil {
		return AdmitResult{}, cerr
	}
	res.Committed = committed
	res.Late = late
	res.Watermark = t.watermark
	// A late event may fill a gap whose hole was declared earlier.
	if gk, ok := t.gapByIdentity(e.Identity()); ok {
		g := t.gaps[gk]
		g.Filled = true
		t.gaps[gk] = g
		res.FilledGaps++
	}
	t.stats.Admitted++
	return res, nil
}

// MarkGap records explicit missing data. Gaps never become events.
func (t *Timeline) MarkGap(g events.Gap) *events.Error {
	if g.Track == "" {
		return events.NewError(events.CodeMalformed, g.Track, g.Seq, -1, "gap", "gap has no track", nil)
	}
	if g.Reason == "" {
		g.Reason = "unspecified"
	}
	if g.Kind == "" {
		g.Kind = events.KindRTP
	}
	key := gapKey{track: g.Track, kind: g.Kind, seq: g.Seq}
	if existing, ok := t.gaps[key]; ok {
		if existing.Filled {
			// A filled gap must not be reopened by a redundant report.
			return nil
		}
		return nil
	}
	t.gaps[key] = g
	t.stats.Gaps++
	return nil
}

// Advance moves the watermark to now-lateWindow (if that is forward) and
// commits the newly final events.
func (t *Timeline) Advance(nowNS int64) (CommitResult, *events.Error) {
	if nowNS < -clock.MaxTimelineNS || nowNS > clock.MaxTimelineNS {
		return CommitResult{}, events.NewError(events.CodeTimeOutOfRange, "", 0, -1, "advance",
			fmt.Sprintf("advance time %d outside timeline", nowNS), nil)
	}
	next := nowNS - t.cfg.LateWindowNS
	if next > t.watermark {
		t.watermark = next
	}
	committed, err := t.commit()
	return CommitResult{Committed: committed, Watermark: t.watermark}, err
}

// Flush finalises every accepted event, including pending ones, and returns
// the complete committed set.
func (t *Timeline) Flush() (CommitResult, *events.Error) {
	if t.hasAt {
		t.watermark = t.maxAt
	} else {
		t.watermark = 0
	}
	committed, err := t.commit()
	return CommitResult{Committed: committed, Watermark: t.watermark}, err
}

// Committed returns the committed event prefix in canonical order.
func (t *Timeline) Committed() []events.Event {
	return append([]events.Event(nil), t.accepted[:t.committedPrefix]...)
}

// Pending returns admitted-but-not-yet-final events in canonical order.
func (t *Timeline) Pending() []events.Event {
	return append([]events.Event(nil), t.accepted[t.committedPrefix:]...)
}

// Gaps returns gap records, optionally only unresolved ones.
func (t *Timeline) Gaps(unresolvedOnly bool) []events.Gap {
	out := make([]events.Gap, 0, len(t.gaps))
	for _, g := range t.gaps {
		if unresolvedOnly && g.Filled {
			continue
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return events.CompareGaps(out[i], out[j]) < 0 })
	return out
}

// StateBytes serialises the canonical state: watermark, committed events and
// unresolved gaps. It is independent of arrival order.
func (t *Timeline) StateBytes() []byte {
	return events.StateBytes(t.watermark, t.Committed(), t.Gaps(true))
}

// StateHash renders StateBytes as a hex SHA-256 digest.
func (t *Timeline) StateHash() string {
	sum := sha256.Sum256(t.StateBytes())
	return hex.EncodeToString(sum[:])
}

func (t *Timeline) find(e events.Event) (int, bool) {
	i := sort.Search(len(t.accepted), func(i int) bool { return events.CompareEvents(t.accepted[i], e) >= 0 })
	return i, i < len(t.accepted)
}

func (t *Timeline) insertAt(i int, e events.Event) {
	t.accepted = append(t.accepted, events.Event{})
	copy(t.accepted[i+1:], t.accepted[i:])
	t.accepted[i] = e
	t.digests[e.Identity()] = canonicalDigest(e)
}

// canonicalDigest hashes the full canonical event record, so a conflicting
// re-delivery of the same identity (different time, text or payload) is
// detected rather than silently treated as a duplicate.
func canonicalDigest(e events.Event) [sha256.Size]byte {
	var b bytes.Buffer
	events.AppendEventCanonical(&b, e)
	return sha256.Sum256(b.Bytes())
}

// gapByIdentity finds a gap record whose identity matches an event.
func (t *Timeline) gapByIdentity(id events.Identity) (gapKey, bool) {
	k := gapKey{track: id.Track, kind: id.Kind, seq: id.Seq}
	if _, ok := t.gaps[k]; ok {
		return k, true
	}
	// RTP packets are identified by KindRTP in the stream but their timeline
	// event may share the same track/seq; accept the RTP gap kind as a match.
	k2 := gapKey{track: id.Track, kind: events.KindRTP, seq: id.Seq}
	if _, ok := t.gaps[k2]; ok {
		return k2, true
	}
	return gapKey{}, false
}

func (t *Timeline) advanceStats() {
	t.stats.Pending = len(t.accepted) - t.committedPrefix
	t.stats.Watermark = t.watermark
	t.stats.MaxEventAt = t.maxAt
}

// commit moves the committed prefix forward over every accepted event whose
// time is final, taking checkpoints along the way.
func (t *Timeline) commit() ([]events.Event, *events.Error) {
	from := t.committedPrefix
	for t.committedPrefix < len(t.accepted) && t.accepted[t.committedPrefix].At <= t.watermark {
		t.committedPrefix++
	}
	if t.committedPrefix-from > 0 {
		t.stats.Committed += t.committedPrefix - from
	}
	if err := t.takeCheckpointIfDue(); err != nil {
		return nil, err
	}
	if pending := len(t.accepted) - t.committedPrefix; pending > t.cfg.MaxPending {
		return nil, events.NewError(events.CodeCapacity, "", 0, -1, "commit",
			fmt.Sprintf("pending limit %d reached (%d buffered)", t.cfg.MaxPending, pending), nil)
	}
	t.advanceStats()
	return append([]events.Event(nil), t.accepted[from:t.committedPrefix]...), nil
}
