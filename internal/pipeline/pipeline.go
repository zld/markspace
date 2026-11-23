package pipeline

import (
	"bytes"
	"fmt"

	"mediatool/internal/mapping"
	"mediatool/internal/mp4"
	"mediatool/internal/remux"
	"mediatool/internal/timeline"
)

// Input is one fragment delivery: raw bytes plus the RTP timing context
// when the fragment was carried over RTP.
type Input struct {
	Data      []byte
	HasRTP    bool
	RTPWallMS int64
	RTPMedia  uint64
}

// Counters aggregates pipeline statistics for the summary and metrics.
type Counters struct {
	TotalInputs      int
	RejectedBoxes    int
	RejectedFragments int
	DuplicateSeq     int
	MissingMfhd      int
	LateFragments    int
	LateRecomputed   int
	Wraparounds      int
	Restarts         int
	SampleCount      int
	PayloadBytes     int
}

// Result is the deterministic output of one pipeline run.
type Result struct {
	Manifest    *remux.Manifest
	Counters    Counters
	Errors      []*timeline.MediaError
	ClockReport []ClockReport
	Events      []*timeline.SampleEvent
	OutputDir   string
}

// Pipeline wires the subsystems together for one run.
type Pipeline struct {
	mapper  *mapping.Mapper
	tracks  map[uint32]mp4.TrackInfo
	clocks  map[uint32]*TrackClock
	engines map[uint32]*timeline.Engine
	recomp  map[uint32]*timeline.Recomputer
	seenSeq map[uint32]map[int]int // track -> seq -> first InputOrder
	writer  *remux.Writer
	order   int
	cnt     Counters
	errs    []*timeline.MediaError
	reports []ClockReport
	events  []*timeline.SampleEvent
}

// New builds a pipeline. rules map parsed tracks to output slots; outDir
// is the validated root for all output writes.
func New(rules []mapping.Rule, outDir string) (*Pipeline, error) {
	w, err := remux.NewWriter(outDir)
	if err != nil {
		return nil, err
	}
	return &Pipeline{
		mapper:  mapping.NewMapper(rules),
		tracks:  make(map[uint32]mp4.TrackInfo),
		clocks:  make(map[uint32]*TrackClock),
		engines: make(map[uint32]*timeline.Engine),
		recomp:  make(map[uint32]*timeline.Recomputer),
		seenSeq: make(map[uint32]map[int]int),
		writer:  w,
	}, nil
}

// SetTracks registers init-segment track metadata.
func (p *Pipeline) SetTracks(tracks []mp4.TrackInfo) {
	for _, tr := range tracks {
		p.tracks[tr.TrackID] = tr
	}
	p.mapper.SetTracks(tracks)
}

// Ingest parses and processes one fragment delivery. Fatal parse errors
// count as rejected boxes and do not stop the run.
func (p *Pipeline) Ingest(in Input) {
	p.cnt.TotalInputs++
	inputOrder := p.order
	p.order++

	frag, perr := mp4.ParseFragment(bytes.NewReader(in.Data), 0)
	if perr != nil {
		p.cnt.RejectedBoxes++
		p.errs = append(p.errs, timeline.AsMediaError(perr))
		return
	}
	p.errs = append(p.errs, frag.Errors...)
	if frag.Rejected {
		p.cnt.RejectedBoxes++
		p.cnt.RejectedFragments++
		return
	}
	// Independent cross-check with the go-mp4 structural reader. A
	// disagreement means our hand-written scanner accepted something the
	// reference parser rejects; treat it as a rejected box.
	if _, verr := mp4.AbemaValidate(in.Data); verr != nil {
		p.cnt.RejectedBoxes++
		p.errs = append(p.errs, timeline.NewError(timeline.ClassBoxTruncated,
			"go-mp4 cross-validation failed: "+verr.Error()))
		return
	}
	for _, run := range frag.Runs {
		p.ingestRun(inputOrder, frag, run, in)
	}
}

func (p *Pipeline) ingestRun(inputOrder int, frag *mp4.Fragment,
	run *mp4.TrackRun, in Input) {
	track, ok := p.tracks[run.TrackID]
	if !ok {
		p.errs = append(p.errs, timeline.NewError(timeline.ClassMappingConflict,
			fmt.Sprintf("fragment references unknown track id %d", run.TrackID)))
		return
	}
	clock := p.clocks[run.TrackID]
	if clock == nil {
		clock = NewTrackClock(run.TrackID, track.Timescale)
		p.clocks[run.TrackID] = clock
		p.engines[run.TrackID] = timeline.NewEngine(run.TrackID)
		p.recomp[run.TrackID] = timeline.NewRecomputer(run.TrackID)
		p.seenSeq[run.TrackID] = make(map[int]int)
	}

	// mfhd handling: missing sequence numbers are tolerated and time
	// continuity is preserved by the clock; duplicates are marked.
	seq := frag.Seq
	repeated := false
	if seq < 0 {
		p.cnt.MissingMfhd++
	} else if first, dup := p.seenSeq[run.TrackID][seq]; dup {
		repeated = true
		p.cnt.DuplicateSeq++
		p.errs = append(p.errs, &timeline.MediaError{
			Class: timeline.ClassDuplicateSequence, Offset: -1,
			TrackID: run.TrackID,
			Detail: fmt.Sprintf("seq %d repeats input order %d (first seen at %d)",
				seq, inputOrder, first),
		})
	} else {
		p.seenSeq[run.TrackID][seq] = inputOrder
	}

	duration := runDurations(run)
	ver := run.TfdtVersion
	if !run.TfdtPresent {
		// No tfdt: continue from the clock's expected position.
		run.BaseMediaDecode = clock.ExpectedNext()
		ver = 1
	}
	report := clock.Correct(run.BaseMediaDecode, ver, duration,
		in.RTPWallMS, in.RTPMedia, in.HasRTP)
	if report.Wraparound {
		p.cnt.Wraparounds++
	}
	if report.Restart {
		p.cnt.Restarts++
	}
	p.reports = append(p.reports, report)

	late, decision := p.engines[run.TrackID].Push(report.CorrectedDTS)
	if late {
		p.cnt.LateFragments++
		p.errs = append(p.errs, &timeline.MediaError{
			Class: timeline.ClassLateFragment, Offset: -1, TrackID: run.TrackID,
			Detail: fmt.Sprintf("late dts=%d <= watermark=%d",
				report.CorrectedDTS, decision.Watermark),
		})
		_, rng := p.recomp[run.TrackID].Recompute(*decision)
		p.cnt.LateRecomputed += rng.AffectedCount
	}

	_, payloadHash, err := frag.RunPayload(runIndex(frag, run.TrackID))
	if err != nil {
		p.errs = append(p.errs, timeline.AsMediaError(err))
		return
	}
	hashes := make([][32]byte, len(run.SampleSizes))
	for i := range run.SampleSizes {
		_, h, herr := frag.SamplePayload(runIndex(frag, run.TrackID), i)
		if herr != nil {
			p.errs = append(p.errs, timeline.AsMediaError(herr))
			return
		}
		hashes[i] = h
	}
	ev := &timeline.SampleEvent{
		TrackID: run.TrackID, MediaType: track.MediaType,
		CodecKey: track.CodecKey, Timescale: track.Timescale,
		Seq: seq, InputOrder: inputOrder, DTS: run.BaseMediaDecode,
		SampleDurations: append([]uint32(nil), run.SampleDurations...),
		SampleSizes:     append([]uint32(nil), run.SampleSizes...),
		SampleHashes:    hashes, PayloadHash: payloadHash,
		Repeated: repeated, Late: late,
		CorrectedDTS: report.CorrectedDTS,
		CorrectionApplied: report.Correction,
		ClockConfidence:   report.Confidence,
	}
	p.events = append(p.events, ev)
	p.recomp[run.TrackID].Add(ev)

	// Only mapped, non-repeated events are remuxed. A repeated fragment is
	// marked deterministically but never overwrites the first copy.
	if repeated {
		return
	}
	slot := p.slotFor(run.TrackID)
	if slot == "" {
		return
	}
	meta := remux.SpoolMeta{
		Slot: slot, OrderKey: inputOrder, InputOrder: inputOrder,
		Seq: seq, TrackID: run.TrackID, CorrectedDTS: report.CorrectedDTS,
		BaseMediaDecode: run.BaseMediaDecode, Timescale: track.Timescale,
		SampleDurations: ev.SampleDurations, SampleSizes: ev.SampleSizes,
		Repeated: repeated, Late: late, PayloadHash: payloadHash,
	}
	if serr := p.writer.Spool(meta, bytes.NewReader(frag.MdatPayload),
		len(frag.MdatPayload)); serr != nil {
		p.errs = append(p.errs, timeline.AsMediaError(serr))
		return
	}
	p.cnt.SampleCount += len(run.SampleSizes)
	p.cnt.PayloadBytes += len(frag.MdatPayload)
}

// slotFor resolves the output slot name for a track id.
func (p *Pipeline) slotFor(trackID uint32) string {
	for _, s := range p.mapper.Recompute() {
		if s.Resolved && s.Track.TrackID == trackID {
			return s.Rule.OutputSlot
		}
	}
	return ""
}

// Finalize resolves the mapping, writes all output streams and returns the
// immutable run result. Unmapped tracks only add diagnostics.
func (p *Pipeline) Finalize() (*Result, error) {
	slots := p.mapper.Recompute()
	for _, f := range p.mapper.Failures() {
		p.errs = append(p.errs, f)
	}
	var plans []remux.FinalizePlan
	metas := p.writer.Metas()
	for _, slot := range slots {
		if !slot.Resolved {
			continue // mapping conflict: this output is stopped, others proceed
		}
		var in []remux.SpoolMeta
		for _, m := range metas {
			if m.Slot == slot.Rule.OutputSlot {
				in = append(in, m)
			}
		}
		plans = append(plans, remux.FinalizePlan{
			Slot: slot.Rule.OutputSlot, Track: slot.Track, Metas: in,
		})
	}
	manifest, err := p.writer.Finalize(plans)
	if err != nil {
		return nil, err
	}
	return &Result{
		Manifest: manifest, Counters: p.cnt, Errors: p.errs,
		ClockReport: p.reports, Events: p.events, OutputDir: p.writer.Root(),
	}, nil
}

func runDurations(run *mp4.TrackRun) uint64 {
	var sum uint64
	for _, d := range run.SampleDurations {
		sum += uint64(d)
	}
	return sum
}

func runIndex(frag *mp4.Fragment, trackID uint32) int {
	for i, r := range frag.Runs {
		if r.TrackID == trackID {
			return i
		}
	}
	return 0
}
