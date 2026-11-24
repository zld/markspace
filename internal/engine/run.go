package engine

import (
	"sort"

	"mediasync/internal/model"
	"mediasync/internal/output"
)

// Run processes every event from a clean state and returns the derived
// delivery. It never consults the wall clock except to measure resume latency,
// which is reported separately and never influences output.
func (e *Engine) Run(in *Input) (*Result, error) {
	if err := e.checkInput(in); err != nil {
		return nil, err
	}
	if err := e.store.Remove(); err != nil {
		return nil, model.NewError(model.CodeCheckpointCorrupt, model.Loc{File: e.store.Dir()}, "reset checkpoint log").Wrap(err)
	}
	e.checkpoints = 0
	return e.runFrom(in, 0)
}

// ResumeFromCheckpoint restores the newest verified checkpoint and processes
// the remaining events. The returned digest matches an uninterrupted run
// because delivery is re-derived from the accepted log prefix plus the tail.
func (e *Engine) ResumeFromCheckpoint(in *Input) (*Result, error) {
	if err := e.checkInput(in); err != nil {
		return nil, err
	}
	rr, err := e.Resume()
	if err != nil {
		return nil, err
	}
	res, rerr := e.runFrom(in, int(rr.ResumedFrom))
	if rerr != nil {
		return nil, rerr
	}
	res.Summary.ResumedFrom = rr.ResumedFrom
	res.Summary.ResumeMS = rr.ResumeMS
	res.ResumedFrom = rr.ResumedFrom
	res.ResumeMS = rr.ResumeMS
	res.FallbackReason = rr.Fallback
	res.Summary.FallbackReason = rr.Fallback
	return res, nil
}

func (e *Engine) checkInput(in *Input) error {
	if in == nil || in.Manifest == nil {
		return model.NewError(model.CodeManifestInvalid, model.Loc{}, "input is nil")
	}
	if in.Manifest != e.man {
		return model.NewError(model.CodeManifestInvalid, model.Loc{}, "input manifest differs from engine manifest")
	}
	return nil
}

// runFrom processes events from the given index. Checkpoints are written on
// the configured interval measured from the absolute event count.
func (e *Engine) runFrom(in *Input, start int) (*Result, error) {
	var errs model.ErrorList
	e.processed = int64(start)
	for i := start; i < len(in.Events); i++ {
		ev := in.Events[i]
		if _, ok := e.tracks[ev.TrackID]; !ok {
			errs.Add(model.NewError(model.CodeManifestInvalid,
				model.Loc{File: e.man.Path(), TrackID: ev.TrackID, HasByteOffset: true, ByteOffset: in.EventBytes[i]},
				"event %d names unknown track %d", i, ev.TrackID))
			e.processed++
			continue
		}
		var err *model.Error
		if ev.IsRepair() {
			err = e.ingestRepair(ev, i)
		} else {
			err = e.ingestMedia(ev, i, in.EventBytes[i])
		}
		if err != nil {
			errs.Add(err)
		}
		e.processed++
		if e.opts.CheckpointEvery > 0 && e.processed%e.opts.CheckpointEvery == 0 {
			if werr := e.writeCheckpoint(in.EventBytes[i]); werr != nil {
				errs.Add(werr)
			}
		}
		if e.opts.InterruptAfter > 0 && e.processed >= e.opts.InterruptAfter {
			e.interrupted = true
			break
		}
	}
	e.Errors = errs
	return e.finish(in)
}

// ingestRepair records an out-of-band redundancy proof. Repairs never produce
// media; they only let classification mark a gap recoverable.
func (e *Engine) ingestRepair(ev model.PacketEvent, index int) *model.Error {
	d := e.dedupers[ev.TrackID]
	if d == nil {
		return model.NewError(model.CodeManifestInvalid,
			model.Loc{File: e.man.Path(), TrackID: ev.TrackID}, "repair names unknown track")
	}
	src := d.RelativeExt(uint64(ev.Seq))
	targets := make([]int64, 0, len(ev.Carries))
	for _, c := range ev.Carries {
		targets = append(targets, d.RelativeExt(uint64(c)))
	}
	e.repairs = append(e.repairs, adminRepair{Event: int64(index), TrackID: ev.TrackID, Source: src, Targets: targets})
	return nil
}

// ingestMedia unwraps, deduplicates and records one media packet. A packet
// whose unrolled timestamp falls before the current watermark is a late
// event: it is still accepted, and its arrival retroactively changes the
// earliest affected checkpoint, so it counts as a recompute.
func (e *Engine) ingestMedia(ev model.PacketEvent, index int, byteOffset int64) *model.Error {
	loc := model.Loc{File: e.man.Path(), TrackID: ev.TrackID, HasByteOffset: true, ByteOffset: byteOffset}
	d := e.dedupers[ev.TrackID]
	tse := e.tsExt[ev.TrackID]
	track := e.tracks[ev.TrackID]
	seq := d.Ingest(rtpHeader(ev))
	e.packets++
	if seq.Duplicate {
		e.dups++
		return nil
	}
	_, ts, _, err := tse.ExtendNanos(uint32(ev.Timestamp))
	if err != nil {
		return model.NewError(model.CodeRTPHeader, loc, "timestamp extension for track %d", ev.TrackID).Wrap(err)
	}
	start, serr := track.Start()
	if serr != nil {
		return model.NewError(model.CodeManifestInvalid, loc, "track start").Wrap(serr)
	}
	// Normalize each track onto the shared base: the first observed timestamp
	// maps onto the track's declared non-zero start, so tracks captured with
	// independent clocks land within a few milliseconds of each other.
	base, ok := e.bases[ev.TrackID]
	if !ok {
		base = start - ts
		e.bases[ev.TrackID] = base
	}
	// A packet delivered out of order can precede the anchor, so the media
	// time is signed and may be slightly negative. That is expected and is
	// resolved by canonical ordering, not by clamping to a fake zero.
	media := ts + base
	late := e.wm.Started() && e.wm.IsLate(media)
	if late {
		e.lateRec++
		e.late = append(e.late, LateEvent{
			TrackID: ev.TrackID, ExtSeq: seq.Ext, Corrected: media,
			AffectedAt: e.wm.Value(), RewindIndex: e.affectedIndex(media),
		})
	}
	// The watermark advances on every accepted event, late or not: a late
	// event can never pull the watermark backwards.
	e.wm.Advance(media)
	if ev.Kind != "" && ev.Kind != track.Kind {
		return model.NewError(model.CodeManifestInvalid, loc,
			"event kind %q differs from manifest kind %q", ev.Kind, track.Kind)
	}
	e.accepted = append(e.accepted, accepted{
		Event: int64(index), TrackID: ev.TrackID, Kind: track.Kind,
		Keyframe: ev.Keyframe, Ext: seq.Ext, Raw: media,
		Arrival: model.Nanos(ev.Arrival), Size: ev.Size,
	})
	return nil
}

// affectedIndex reports how many delivered records would be at or after the
// affected time. It is informational for the late report; the actual rewind
// recomputes from the accepted log, which is equivalent.
func (e *Engine) affectedIndex(affected model.Nanos) int {
	der := deriveRecords(e.accepted, e.repairs, e.tracks, e.order, e.opts)
	n := 0
	for _, r := range der.records {
		if r.Corrected >= affected {
			n++
		}
	}
	return len(der.records) - n
}

// finish derives the delivery, fills the summary and applies the interruption
// scrub. It does not write another checkpoint: an interrupted run must resume
// from the last periodic checkpoint, not from the interruption point.
func (e *Engine) finish(in *Input) (*Result, error) {
	_ = in
	der := deriveRecords(e.accepted, e.repairs, e.tracks, e.order, e.opts)
	digest := output.Digest(der.records)
	res := &Result{
		Records:          der.records,
		Digest:           digest,
		LateEvents:       append([]LateEvent(nil), e.late...),
		LateRecomputes:   e.lateRec,
		CheckpointCount:  e.checkpoints,
		ResumedFrom:      e.resumedFrom,
		ResumeMS:         e.resumeMS,
		FallbackReason:   e.fallbackReason,
		MaxSkew:          der.maxSkew,
		LostByTrack:      map[uint32]int64{},
		RecoveredByTrack: map[uint32]int64{},
	}
	for id, n := range der.lost {
		res.LostByTrack[id] = n
	}
	for id, n := range der.recovered {
		res.RecoveredByTrack[id] = n
	}
	var lost, recovered, unrecoverable int64
	for _, n := range der.lost {
		lost += n
	}
	for _, n := range der.recovered {
		recovered += n
	}
	for _, n := range der.unrecoverable {
		unrecoverable += n
	}
	res.Summary = output.Summary{
		OK:             true,
		Seed:           e.opts.Seed,
		WrapBits:       e.opts.WrapBits,
		PacketTotal:    e.packets,
		PacketsDup:     e.dups,
		Lost:           lost,
		Recovered:      recovered,
		Unrecoverable:  unrecoverable,
		LateRecomputed: e.lateRec,
		LateEvents:     int64(len(e.late)),
		MaxAVSkewMS:    der.maxSkew.MS(),
		DriftPPM:       der.driftPPM,
		DriftClamped:   der.driftClamped,
		Checkpoints:    e.checkpoints,
		ResumedFrom:    e.resumedFrom,
		ResumeMS:       e.resumeMS,
		Digest:         digest,
	}
	// The kernel's own diagnostics are reported by the engine; callers add
	// their own findings on top.
	if n := len(e.Errors); n > 0 {
		for _, me := range e.Errors {
			res.Errors = append(res.Errors, me)
		}
	}
	return res, nil
}

// sortedAccepted returns the accepted log in a canonical order. The log is
// already in ingestion order; sorting by event index is a no-op safeguard
// that makes derivation independent of append details.
func sortedAccepted(a []accepted) []accepted {
	out := append([]accepted(nil), a...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Event < out[j].Event })
	return out
}
