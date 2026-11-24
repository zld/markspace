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
// Every segment starts on a keyframe so each output fragment is independently
// decodable. When the stream has fewer keyframes than requested segments the
// count is reduced rather than emitting a segment that cannot be indexed.
// Within the keyframe constraint starts are spaced as evenly as possible. Frame
// order is preserved and no frame is duplicated or dropped. Audio is assigned
// by the placed timeline tick, so a segment carries the audio that actually
// sounds over its video span.
func selectSegments(recs []*frames.Record, audio []syncer.Placed, count int) []SegmentPlan {
	if len(recs) == 0 || count < 1 {
		return nil
	}
	starts := keyframeStarts(recs)
	if count > len(starts) {
		count = len(starts)
	}
	if count > len(recs) {
		count = len(recs)
	}
	if count < 1 {
		count = 1
	}
	// Pick evenly spaced keyframe starts, always including index zero.
	picked := make([]int, 0, count)
	for i := 0; i < count; i++ {
		pos := i * len(starts) / count
		if pos >= len(starts) {
			pos = len(starts) - 1
		}
		if len(picked) > 0 && starts[pos] <= picked[len(picked)-1] {
			continue
		}
		picked = append(picked, starts[pos])
	}
	plans := make([]SegmentPlan, 0, len(picked))
	for i, start := range picked {
		end := len(recs)
		if i+1 < len(picked) {
			end = picked[i+1]
		}
		if end <= start {
			continue
		}
		plans = append(plans, SegmentPlan{
			Index:     len(plans),
			Frames:    recs[start:end],
			StartTick: int64(recs[start].Meta.PTS),
			EndTick:   segmentEndTick(recs, start, end),
		})
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

// keyframeStarts returns record indices that may begin a segment. Index zero is
// always eligible so the first fragment is never empty, even if the producer
// forgot to flag its first frame.
func keyframeStarts(recs []*frames.Record) []int {
	out := []int{0}
	for i := 1; i < len(recs); i++ {
		if recs[i].Keyframe() {
			out = append(out, i)
		}
	}
	return out
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
// Jitter and recovery share one pass, so both stage summaries are emitted
// here: the jitter summary covers ordering and drops, the recovery summary
// covers hole concealment and incomplete fragments.
func (r *Runner) runAudio() ([]recovery.Unit, jitter.Stats, error) {
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

	recSummary := provenance.StageSummary{
		Name:       "recovery",
		InputHash:  r.hashes.audio,
		ConfigHash: r.cfg.Hash(),
		OutputHash: audioDigest(rep.Units),
		Detail:     "sealed audio gaps",
		Counts: map[string]int{
			"gap_events":  rep.GapEvents,
			"recovered":   rep.Recovered,
			"unrecovered": rep.Unrecovered,
			"faults":      rep.Faults.Len(),
		},
	}
	for _, e := range rep.Faults {
		recSummary.Faults = append(recSummary.Faults, provenance.FromFault(e))
	}
	if err := r.recordStage(recSummary); err != nil {
		return nil, jitter.Stats{}, err
	}
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
