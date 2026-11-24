package pipeline

import (
	"os"
	"path/filepath"
	"sort"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/frames"
	"mediarun/internal/jitter"
	"mediarun/internal/provenance"
	"mediarun/internal/recovery"
	rtpparse "mediarun/internal/rtp"
	"mediarun/internal/syncer"
)

// SegmentPlan is the immutable description of one output segment, computed
// before any bytes are written. Planning first means an interrupted run can
// resume with the same frame allocation and therefore the same segment bytes.
type SegmentPlan struct {
	Index     int
	Frames    []*frames.Record
	Audio     []syncer.Placed
	StartTick int64
	EndTick   int64
}

// selectSegments partitions records and already-placed audio into segments.
//
// Segment boundaries are snapped to keyframes so every output fragment starts
// at a random access point and can be decoded independently; within that
// constraint the split stays as close to equal frame counts as possible. Frame
// order is preserved and no frame is duplicated or dropped. Audio is assigned
// by the placed timeline tick, so a segment carries the audio that actually
// sounds over its video span.
func selectSegments(recs []*frames.Record, audio []syncer.Placed, count int) []SegmentPlan {
	if len(recs) == 0 {
		return nil
	}
	if count > len(recs) {
		count = len(recs)
	}
	bounds := keyframeBounds(recs, count)
	plans := make([]SegmentPlan, 0, len(bounds)-1)
	for i := 0; i+1 < len(bounds); i++ {
		start, end := bounds[i], bounds[i+1]
		if end <= start {
			continue
		}
		plan := SegmentPlan{
			Index:     len(plans),
			Frames:    recs[start:end],
			StartTick: int64(recs[start].Meta.PTS),
			EndTick:   segmentEndTick(recs, start, end),
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return nil
	}
	for _, p := range audio {
		idx := 0
		for i, plan := range plans {
			if p.Tick >= plan.StartTick {
				idx = i
			}
		}
		plans[idx].Audio = append(plans[idx].Audio, p)
	}
	return plans
}

// keyframeBounds returns count+1 record indices delimiting the segments. Index
// zero and len(recs) are always included; interior boundaries snap to the
// nearest keyframe that keeps all segments non-empty and ordered. When the
// stream has too few keyframes the ideal equal split is used instead.
func keyframeBounds(recs []*frames.Record, count int) []int {
	n := len(recs)
	bounds := []int{0}
	if count <= 1 || n <= 1 {
		return append(bounds, n)
	}
	var keys []int
	for i, r := range recs {
		if r.Keyframe() && i > 0 {
			keys = append(keys, i)
		}
	}
	prev := 0
	for seg := 1; seg < count; seg++ {
		ideal := seg * n / count
		if ideal <= prev {
			ideal = prev + 1
		}
		if ideal >= n {
			break
		}
		chosen := ideal
		if len(keys) > 0 {
			best := -1
			for _, k := range keys {
				if k <= prev || k >= n || k <= prev {
					continue
				}
				if best == -1 || absInt(k-ideal) < absInt(best-ideal) ||
					(absInt(k-ideal) == absInt(best-ideal) && k > best) {
					best = k
				}
			}
			// Never pick a boundary that would starve a later segment.
			remainingSegs := count - seg
			if best != -1 && n-best >= remainingSegs {
				chosen = best
			}
		}
		bounds = append(bounds, chosen)
		prev = chosen
	}
	return append(bounds, n)
}

// segmentEndTick is the half-open end of a segment in the 90 kHz domain. The
// final segment extends by one nominal frame step so it is never empty.
func segmentEndTick(recs []*frames.Record, start, end int) int64 {
	if end < len(recs) {
		return int64(recs[end].Meta.PTS)
	}
	if end-start >= 2 {
		step := int64(recs[end-1].Meta.PTS) - int64(recs[end-2].Meta.PTS)
		if step <= 0 {
			step = int64(recs[end-1].Meta.PTS) - int64(recs[start].Meta.PTS)
		}
		return int64(recs[end-1].Meta.PTS) + step
	}
	return int64(recs[end-1].Meta.PTS)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// runParse scans the frame file in metadata-only mode and records the stage
// summary. The returned records own no payloads.
func (r *Runner) runParse() ([]*frames.Record, error) {
	f, err := os.Open(r.inputs.FramesPath)
	if err != nil {
		return nil, fault.Wrap(fault.StageParse, fault.KindIO, err, "open %s", r.inputs.FramesPath)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fault.Wrap(fault.StageParse, fault.KindIO, err, "stat %s", r.inputs.FramesPath)
	}
	sc, err := frames.NewScanner(f, info.Size(), frames.Limits{
		MaxRecords: r.cfg.MaxRecords,
		MaxPayload: r.cfg.MaxFrameBytes,
	})
	if err != nil {
		return nil, err
	}
	sc.SetMetadataOnly(true)
	var recs []*frames.Record
	for {
		rec, err := sc.Next()
		if err != nil {
			if isEOF(err) {
				break
			}
			return nil, err
		}
		meta := rec.Meta
		recs = append(recs, &frames.Record{Offset: rec.Offset, Index: rec.Index, Meta: meta})
	}
	parseFaults := sc.Faults()
	// Records lost to parse or checksum failures are the video-side recovery
	// count: they are reported, never fabricated.
	r.metrics.RecoveredVideo = r.resyncRecoveries(parseFaults, recs)
	outputHash := recordsDigest(recs)
	summary := provenance.StageSummary{
		Name:       "parse",
		InputHash:  r.hashes.frames,
		ConfigHash: r.cfg.Hash(),
		OutputHash: outputHash,
		Detail:     "scanned frame records",
		Counts:     map[string]int{"records": len(recs), "faults": parseFaults.Len()},
	}
	for _, e := range parseFaults {
		summary.Faults = append(summary.Faults, provenance.FromFault(e))
	}
	if err := r.recordStage(summary); err != nil {
		return nil, err
	}
	return recs, nil
}

// resyncRecoveries counts records that were skipped but whose neighbours were
// still located, i.e. successful resynchronisations. A fault at or after the
// last parsed record's end means the tail was lost, so nothing followed it and
// it is a loss rather than a recovery.
func (r *Runner) resyncRecoveries(parseFaults fault.List, recs []*frames.Record) int {
	if parseFaults.Len() == 0 || len(recs) == 0 {
		return 0
	}
	lastEnd := int64(0)
	for _, rec := range recs {
		end := rec.Offset + int64(frames.HeaderSize) + int64(rec.Meta.Size) + frames.DigestSize
		if end > lastEnd {
			lastEnd = end
		}
	}
	n := 0
	for _, e := range parseFaults {
		switch e.Kind {
		case fault.KindTruncated, fault.KindChecksum, fault.KindLength, fault.KindVersion, fault.KindTrack:
			if e.Offset >= 0 && e.Offset < lastEnd {
				n++
			}
		}
	}
	return n
}

// runAudio parses and orders the RTP sidecar, then seals recoverable gaps.
func (r *Runner) runAudio() ([]recovery.Unit, jitter.Stats, error) {
	// The jitter stage records its own summary, so the loss report from the
	// recovery pass is folded into it rather than emitted twice.
	return r.runAudioOnce()
}

func (r *Runner) runAudioOnce() ([]recovery.Unit, jitter.Stats, error) {
	f, err := os.Open(r.inputs.AudioPath)
	if err != nil {
		return nil, jitter.Stats{}, fault.Wrap(fault.StageJitter, fault.KindIO, err, "open %s", r.inputs.AudioPath)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, jitter.Stats{}, fault.Wrap(fault.StageJitter, fault.KindIO, err, "stat %s", r.inputs.AudioPath)
	}
	pkts, parseFaults := rtpparse.ParseSidecar(f, info.Size())
	buf, err := jitter.New(jitter.Config{Window: r.cfg.JitterWindow, MaxPackets: r.cfg.JitterMax})
	if err != nil {
		return nil, jitter.Stats{}, err
	}
	var ordered []*rtpparse.Packet
	profileFaults := fault.List{}
	for _, p := range pkts {
		if err := rtpparse.ValidateProfile(p, r.cfg.AudioPayloadTyp, r.cfg.AudioSSRC); err != nil {
			profileFaults.Add(err)
			continue
		}
		ordered = append(ordered, buf.Push(p).Committed...)
	}
	ordered = append(ordered, buf.Flush()...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ExtSeq < ordered[j].ExtSeq })

	rep, err := recovery.Recover(ordered, r.cfg.RecoveryPolicy())
	if err != nil {
		return nil, jitter.Stats{}, err
	}
	allFaults := append(fault.List{}, parseFaults...)
	allFaults = append(allFaults, profileFaults...)
	allFaults = append(allFaults, buf.Faults()...)
	allFaults = append(allFaults, rep.Faults...)
	r.runtime.audioFaults = allFaults

	stats := buf.Stats()
	outputHash := audioDigest(rep.Units)
	summary := provenance.StageSummary{
		Name:       "jitter",
		InputHash:  r.hashes.audio,
		ConfigHash: r.cfg.Hash(),
		OutputHash: outputHash,
		Detail:     "ordered rtp packets and sealed gaps",
		Counts: map[string]int{
			"packets":      stats.Received,
			"committed":    stats.Committed,
			"duplicates":   stats.Duplicates,
			"late":         stats.Late,
			"overflow":     stats.Overflow,
			"out_of_order": stats.OutOfOrder(),
			"recovered":    rep.Recovered,
			"incomplete":   rep.Unrecovered,
			"faults":       allFaults.Len(),
		},
	}
	for _, e := range allFaults {
		summary.Faults = append(summary.Faults, provenance.FromFault(e))
	}
	if err := r.recordStage(summary); err != nil {
		return nil, jitter.Stats{}, err
	}
	r.runtime.audioIncomplete = rep.Incomplete
	return rep.Units, stats, nil
}

// alignSegment runs the sync stage for one segment over pre-placed audio.
func (r *Runner) alignSegment(plan *SegmentPlan, tl *clock.Timeline) (*syncer.Report, error) {
	rep, err := syncer.Align(plan.Frames, plan.Audio, tl, r.syncConfig())
	if err != nil {
		return nil, err
	}
	for _, e := range r.runtime.placeFaults {
		rep.Faults = append(rep.Faults, e)
	}
	return rep, nil
}

// segmentPath returns the output MP4 path for a segment index.
func segmentPath(dir string, index int) string {
	return filepath.Join(dir, SegmentsDir, segmentName(index))
}

func segmentName(index int) string {
	return "segment-" + pad2(index) + ".mp4"
}

func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
