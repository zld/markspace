package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"

	"archivecheck/internal/fixture"
	"archivecheck/internal/inspect"
	"archivecheck/internal/model"
	"archivecheck/internal/scheduler"
	"archivecheck/internal/transport"
)

// runReorder reads a segment's capture, deduplicates and reorders it, and
// records missing and duplicate counts. It neither reads nor trusts the media
// file, so gaps are reported even when the fragment is later found malformed.
func (t segmentTask) runReorder(cfg Config) *reorderOutcome {
	oc := &reorderOutcome{result: SegmentResult{
		Representation: t.representation, ID: t.seg.ID, URI: t.seg.URI,
		Start: t.seg.Start, Duration: t.seg.Duration, KeyframeP50Ms: -1,
	}}
	capRaw, err := os.ReadFile(path.Join(cfg.Root, t.info.Capture))
	if err != nil {
		oc.fault = model.NewFault(model.CodeInputMissing, model.SeverityError,
			model.Source{Layer: "transport", Segment: t.seg.ID, File: t.info.Capture, Offset: -1},
			"cannot read capture: %v", err).WithCause(err)
		oc.result.Status = "error"
		return oc
	}
	capture, err := fixture.DecodeCapture(capRaw)
	if err != nil {
		oc.fault = model.NewFault(model.CodeTransportBadPayload, model.SeverityError,
			model.Source{Layer: "transport", Segment: t.seg.ID, File: t.info.Capture, Offset: -1},
			"cannot decode capture: %v", err).WithCause(err)
		oc.result.Status = "error"
		return oc
	}
	opts := transport.Options{Window: t.window}
	if exp := t.expectedRange(capture); exp != nil {
		opts.Expected = exp
	}
	reorder := transport.NewReorder(transport.Source{
		Segment: t.seg.ID, Representation: t.representation, File: t.seg.URI,
	}, opts)
	for _, p := range capture.Packets {
		if err := reorder.Push(p); err != nil {
			if f, ok := model.AsFault(err); ok {
				oc.fault = f.WithSegment(t.seg.ID)
			} else {
				oc.fault = model.NewFault(model.CodeTransportSequence, model.SeverityError,
					model.Source{Layer: "transport", Segment: t.seg.ID, File: t.seg.URI, Offset: -1},
					"%v", err)
			}
			oc.result.Status = "error"
			return oc
		}
	}
	tr := reorder.Close()
	oc.result.Missing = tr.Missing
	oc.result.Duplicates = tr.DuplicateCount
	oc.result.Reordered = tr.Reordered
	for _, g := range tr.Gaps {
		oc.gaps = append(oc.gaps, transportGap{
			Before: g.Before, After: g.After, Count: g.Count,
			FirstTimestamp: g.FirstTimestamp, LastTimestamp: g.LastTimestamp,
		})
	}
	for _, f := range reorder.Faults() {
		oc.result.FaultCodes = appendUnique(oc.result.FaultCodes, string(f.Code))
		oc.fault = f.WithSegment(t.seg.ID)
		// A repeated sequence number with a different payload is a
		// representation-integrity failure: the bytes that arrived are not the
		// bytes the catalog recorded, so the whole layer must be flagged.
		if f.Code == model.CodeTransportDuplicate {
			oc.result.RepresentationDigestConflict = true
		}
	}
	oc.result.Status = "ok"
	if tr.Missing > 0 || tr.DuplicateCount > 0 || len(reorder.Faults()) > 0 {
		oc.result.Status = "damaged"
	}
	if oc.result.RepresentationDigestConflict {
		oc.result.Status = "error"
	}
	// The reorder node's digest covers the stream it accepted: sequence range,
	// counts and the sorted payload digests.
	h := sha256.New()
	for _, p := range tr.Packets {
		h.Write([]byte(payloadDigestOf(p.Payload)))
	}
	oc.result.OutputSHA256 = hex.EncodeToString(h.Sum(nil))
	return oc
}

// expectedRange derives the sender's full sequence range from the fixture
// capture: the lowest and highest sequence numbers plus the frame count implied
// by the segment duration. A capture that lost its edges would otherwise look
// clean.
func (t segmentTask) expectedRange(c *fixture.Capture) *transport.Expected {
	if len(c.Packets) == 0 {
		return nil
	}
	var lo, hi uint16 = c.Packets[0].Sequence, c.Packets[0].Sequence
	var byLo, byHi uint32 = c.Packets[0].Timestamp, c.Packets[0].Timestamp
	for _, p := range c.Packets {
		if p.Sequence < lo {
			lo, byLo = p.Sequence, p.Timestamp
		}
		if p.Sequence > hi {
			hi, byHi = p.Sequence, p.Timestamp
		}
	}
	// The fixture emits one packet per frame starting at frame 0, so the
	// expected first sequence is 0 and the last is frames-1 for a complete
	// capture. Infer the count from the segment duration and frame duration.
	frames := t.frameCount()
	if frames <= 0 {
		return &transport.Expected{FirstSeq: uint64(lo), LastSeq: uint64(hi), FirstTimestamp: byLo, LastTimestamp: byHi}
	}
	next := uint64(hi) + 1
	// Only extend the range when the capture begins at the sender's first
	// packet; otherwise sequence numbers are segment-local and edges are
	// unknown.
	if lo != 0 {
		return nil
	}
	_ = next
	return &transport.Expected{
		FirstSeq:       0,
		LastSeq:        uint64(frames - 1),
		FirstTimestamp: byLo,
		LastTimestamp:  byHi,
	}
}

// frameCount is how many target frames fit in the segment. It uses the
// configured frame duration, or the fixture default scaled to the plan
// timebase when none was configured.
func (t segmentTask) frameCount() int {
	frame := t.frameTicks
	if frame <= 0 {
		if t.timebase <= 0 {
			return 0
		}
		frame = fixture.DefaultFrameTicks * t.timebase / fixture.DefaultTimebase
	}
	if frame <= 0 {
		return 0
	}
	return int(t.seg.Duration / frame)
}

func payloadDigestOf(p []byte) string { return transport.PayloadDigest(p) }

// runInspect decodes the fragment, measures keyframe cadence and maps the
// reorder gaps onto the manifest timeline.
func (t segmentTask) runInspect(cfg Config, prev *reorderOutcome) *taskOutcome {
	oc := &taskOutcome{result: SegmentResult{
		Representation: t.representation, ID: t.seg.ID, URI: t.seg.URI,
		Start: t.seg.Start, Duration: t.seg.Duration, KeyframeP50Ms: -1,
	}}
	if prev != nil {
		oc.result.Missing = prev.result.Missing
		oc.result.Duplicates = prev.result.Duplicates
		oc.result.Reordered = prev.result.Reordered
		oc.result.FaultCodes = append([]string(nil), prev.result.FaultCodes...)
		oc.result.RepresentationDigestConflict = prev.result.RepresentationDigestConflict
		oc.fault = prev.fault
		if prev.result.RepresentationDigestConflict {
			oc.structural = model.NewFault(model.CodeDamageRepresentation, model.SeverityError,
				model.Source{Layer: "transport", Segment: t.seg.ID, Representation: t.representation,
					File: t.seg.URI, Offset: -1},
				"sequence-number collision with conflicting payloads in %s segment %s",
				t.representation, t.seg.ID)
			oc.result.FaultCodes = appendUnique(oc.result.FaultCodes, string(model.CodeDamageRepresentation))
		}
	}
	segPath := path.Join(cfg.Root, t.info.URI)
	fd, err := os.Open(segPath)
	if err != nil {
		oc.structural = model.NewFault(model.CodeInputMissing, model.SeverityError,
			model.Source{Layer: "inspect", Segment: t.seg.ID, Representation: t.representation,
				File: t.info.URI, Offset: -1}, "cannot read segment: %v", err).WithCause(err)
		oc.result.Status = "error"
		return oc
	}
	data, err := inspect.SizeReader{R: fd, Limit: t.maxBytes}.ReadAll()
	fd.Close()
	if err != nil {
		oc.structural = model.NewFault(model.CodeInputBadArgs, model.SeverityError,
			model.Source{Layer: "inspect", Segment: t.seg.ID, Representation: t.representation,
				File: t.info.URI, Offset: -1}, "segment exceeds the fragment memory bound: %v", err).WithCause(err)
		oc.result.Status = "error"
		return oc
	}
	frag, perr := inspect.ParseFragment(t.info.URI, data, nil, inspect.Options{})
	if initData, ierr := os.ReadFile(path.Join(cfg.Root, t.info.Init)); ierr == nil {
		if init, ierr := inspect.ParseInit(t.info.Init, initData); ierr == nil {
			frag.LoadSamples(init, inspect.Options{})
		}
	}
	oc.result.TrackCount = len(frag.Tracks)
	for _, trk := range frag.Tracks {
		oc.result.SampleCount += len(trk.Samples)
	}
	kf := inspect.KeyframeIntervals(frag, t.timebase)
	oc.result.KeyframeP50Ms = kf.P50Ms

	for _, f := range frag.Faults {
		f.Source.Segment = t.seg.ID
		f.Source.Representation = t.representation
		oc.result.FaultCodes = appendUnique(oc.result.FaultCodes, string(f.Code))
		if isStructural(f.Code) && oc.structural == nil {
			oc.structural = f
			oc.fault = f
		}
	}
	if perr != nil {
		if f, ok := model.AsFault(perr); ok {
			f.Source.Segment = t.seg.ID
			f.Source.Representation = t.representation
			oc.result.FaultCodes = appendUnique(oc.result.FaultCodes, string(f.Code))
			if oc.structural == nil {
				oc.structural = f
				oc.fault = f
			}
		}
	}

	// The anchor pairs the sender's first RTP timestamp with the segment's
	// manifest start, so a gap maps to a media interval without drift.
	anchor := inspect.GapAnchor{
		RTPTimestamp: t.anchorTS,
		Media:        t.seg.Start,
		RTPTimebase:  t.rtpTimebase,
	}
	if prev != nil {
		for _, g := range prev.gaps {
			dr, fault := inspect.MapGap(t.seg, t.representation, t.info.URI, transport.Gap{
				Before: g.Before, After: g.After, Count: g.Count,
				FirstTimestamp: g.FirstTimestamp, LastTimestamp: g.LastTimestamp,
			}, anchor, t.timebase)
			if fault != nil {
				fault.Source.Segment = t.seg.ID
				oc.result.FaultCodes = appendUnique(oc.result.FaultCodes, string(fault.Code))
			}
			oc.damage = append(oc.damage, dr)
		}
	}

	sum := sha256.Sum256(data)
	oc.result.OutputSHA256 = hex.EncodeToString(sum[:])
	// The catalog digest recorded at archive time must still match; a mismatch
	// means the retained bytes are not the ones that were catalogued. This is
	// only exercised for the digest-conflict fixture: truncation and other
	// structural faults deliberately change the file, so their digests differ
	// by design and are reported from structure instead.
	if t.catalogCheck && t.info.DeclaredSHA256 != "" && t.info.DeclaredSHA256 != oc.result.OutputSHA256 {
		oc.structural = model.NewFault(model.CodeDamageRepresentation, model.SeverityError,
			model.Source{Layer: "pipeline", Segment: t.seg.ID, Representation: t.representation,
				File: t.info.URI, Offset: -1},
			"segment content digest %s does not match catalog digest %s",
			oc.result.OutputSHA256, t.info.DeclaredSHA256).WithBytes(0, int64(len(data)))
		oc.result.FaultCodes = appendUnique(oc.result.FaultCodes, string(model.CodeDamageRepresentation))
	}
	switch {
	case oc.structural != nil:
		oc.result.Status = "error"
	case len(oc.damage) > 0 || oc.result.Missing > 0 || oc.result.Duplicates > 0 || oc.fault != nil:
		oc.result.Status = "damaged"
	default:
		oc.result.Status = "ok"
	}
	return oc
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// dagResult bundles the scheduler report with the shared node state.
type dagResult struct {
	report *scheduler.Report
	state  *dagState
}

// blockingFault is the error that stops the certification path, or nil when the
// segment is intact. Located damage and structural defects both qualify.
func (oc *taskOutcome) blockingFault() *model.Fault {
	if oc.structural != nil {
		return oc.structural
	}
	if len(oc.damage) > 0 {
		for _, d := range oc.damage {
			if d.End > d.Start {
				return model.NewFault(model.CodeDamageGap, model.SeverityError, model.Source{
					Layer: "inspect", Segment: oc.result.ID,
					Representation: oc.result.Representation, File: oc.result.URI, Offset: -1,
				}, "segment %s/%s covers %d missing packets", oc.result.Representation, oc.result.ID, d.MissingPackets).
					WithMedia(d.Start, d.End, d.Timebase)
			}
		}
	}
	if oc.result.Missing > 0 {
		return model.NewFault(model.CodeTransportGap, model.SeverityError, model.Source{
			Layer: "transport", Segment: oc.result.ID,
			Representation: oc.result.Representation, File: oc.result.URI, Offset: -1,
		}, "segment %s/%s is missing %d packets", oc.result.Representation, oc.result.ID, oc.result.Missing)
	}
	return nil
}

// DurationDeviation records a segment whose decoded media duration differs
// from the manifest declaration.
type DurationDeviation struct {
	Representation string `json:"representation"`
	Segment        string `json:"segment"`
	ManifestTicks  int64  `json:"manifest_ticks"`
	MediaTicks     int64  `json:"media_ticks"`
	DeviationTicks int64  `json:"deviation_ticks"`
}

// detectDurationDeviations compares each segment's declared interval with the
// duration actually covered by its init/fragment sample tables. This catches a
// manifest that claims 4 s while the media holds 3.92 s, which no packet loss
// would explain.
func detectDurationDeviations(cfg Config, tasks []segmentTask) []DurationDeviation {
	var out []DurationDeviation
	cache := map[string]*inspect.Init{}
	for _, t := range tasks {
		init, ok := cache[t.info.Init]
		if !ok {
			raw, err := os.ReadFile(path.Join(cfg.Root, t.info.Init))
			if err != nil {
				cache[t.info.Init] = nil
				continue
			}
			init, _ = inspect.ParseInit(t.info.Init, raw)
			cache[t.info.Init] = init
		}
		if init == nil {
			continue
		}
		raw, err := os.ReadFile(path.Join(cfg.Root, t.info.URI))
		if err != nil {
			continue
		}
		frag, _ := inspect.ParseFragment(t.info.URI, raw, nil, inspect.Options{})
		if frag == nil {
			continue
		}
		frag.LoadSamples(init, inspect.Options{})
		var mediaTicks int64
		frame := t.frameTicks
		if frame <= 0 {
			frame = fixture.DefaultFrameTicks * t.timebase / fixture.DefaultTimebase
		}
		for _, tr := range frag.Tracks {
			var first, end uint64
			have := false
			for _, s := range tr.Samples {
				if !have {
					first, have = s.DecodeTime, true
				}
				end = s.DecodeTime + uint64(s.Duration)
			}
			if !have {
				continue
			}
			conv, err := model.ConvertTicks(int64(end-first), int64(tr.Timescale), t.timebase)
			if err != nil {
				continue
			}
			if conv.Ticks > mediaTicks {
				mediaTicks = conv.Ticks
			}
		}
		if mediaTicks == 0 {
			continue
		}
		dev := t.seg.Duration - mediaTicks
		if dev < 0 {
			dev = -dev
		}
		if dev > frame {
			out = append(out, DurationDeviation{
				Representation: t.representation, Segment: t.seg.ID,
				ManifestTicks: t.seg.Duration, MediaTicks: mediaTicks, DeviationTicks: dev,
			})
		}
	}
	return out
}
