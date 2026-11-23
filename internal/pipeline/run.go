// Package pipeline wires parsing, clock correction, cutting, silence detection,
// deduplication and indexing into one resumable batch run.
package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"avbatch/internal/audio"
	"avbatch/internal/clock"
	"avbatch/internal/cmaf"
	"avbatch/internal/fingerprint"
	"avbatch/internal/index"
	"avbatch/internal/mediaerr"
	"avbatch/internal/segment"

	"github.com/pion/rtp"
)

// Config is the run configuration. The media-affecting fields are part of the
// input digest; the block size is too, because it changes the checkpoint
// layout and therefore what a resume can reuse.
type Config struct {
	BlockSamples      int
	SegmentMillis     int64
	SearchMillis      int64
	WindowMillis      int64
	SilenceDBFS       float64
	MinSilenceMillis  int64
	RTPClockRate      uint32
	RTPToleranceTicks uint32
	IndexPath         string
	// CrashAtBlocks makes the run simulate a crash after this many blocks.
	CrashAtBlocks int
}

// DefaultConfig returns the acceptance defaults.
func DefaultConfig() Config {
	return Config{
		BlockSamples:      128,
		SegmentMillis:     1000,
		SearchMillis:      250,
		WindowMillis:      20,
		SilenceDBFS:       -60,
		MinSilenceMillis:  200,
		RTPClockRate:      48000,
		RTPToleranceTicks: 0,
		CrashAtBlocks:     0,
	}
}

// FragmentInput is one fragment with its capture provenance.
type FragmentInput struct {
	Ordinal    int
	Data       []byte
	RTPPackets [][]byte
	// WallNanos is the sender clock at fragment arrival. It is excluded from
	// the input digest: it describes the network, not the media.
	WallNanos int64
}

// Input is one capture group.
type Input struct {
	Init       []byte
	Fragments  []FragmentInput
	AudioPCM   audio.PCM
	TargetKbps float64
	Faults     []string
}

// Result is everything the CLI reports.
type Result struct {
	Segments         int
	SilenceMillis    int64
	Duplicates       int
	Collisions       int
	TargetKbps       float64
	ActualKbps       float64
	DeviationPct     float64
	QualitySamples   int
	AlignmentErrorMS float64
	RecoveredBlocks  int
	ResumeOffset     int64
	ManifestSHA256   string
	Entries          []index.Entry
	Faults           []mediaerr.Kind
	Errors           error
	Recovered        bool
	DriftPPM         float64
	WrapCount        int
	RTPPackets       int
	RTPAnomalies     int
	TruncatedInput   bool
	ResumeRefused    bool
}

// Digest computes the input digest: init bytes, fragment bytes, audio and the
// media-affecting config.
func Digest(in Input, cfg Config) [32]byte {
	h := sha256.New()
	h.Write([]byte("avbatch-v1"))
	h.Write(in.Init)
	for _, f := range in.Fragments {
		sum := sha256.Sum256(f.Data)
		h.Write(sum[:])
	}
	if len(in.AudioPCM.Frames) > 0 {
		buf := make([]byte, len(in.AudioPCM.Frames)*2)
		for i, v := range in.AudioPCM.Frames {
			buf[i*2] = byte(uint16(v) >> 8)
			buf[i*2+1] = byte(uint16(v))
		}
		sum := sha256.Sum256(buf)
		h.Write(sum[:])
	}
	// encoding/json orders struct fields deterministically.
	cfgJSON, _ := json.Marshal(struct {
		BlockSamples     int
		SegmentMillis    int64
		SearchMillis     int64
		WindowMillis     int64
		SilenceDBFS      float64
		MinSilenceMillis int64
		RTPClockRate     uint32
	}{cfg.BlockSamples, cfg.SegmentMillis, cfg.SearchMillis, cfg.WindowMillis,
		cfg.SilenceDBFS, cfg.MinSilenceMillis, cfg.RTPClockRate})
	h.Write(cfgJSON)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// trackSample is one video sample on the corrected timeline, before cutting.
type trackSample struct {
	ordinal  int
	index    int
	start    int64
	dur      int64
	size     int
	digest   [32]byte
	keyframe bool
}

// Run executes the pipeline. A non-nil error always comes with a Result; when
// Result.Recovered is true the output is usable and the error describes input
// damage rather than a failed run.
func Run(in Input, cfg Config) (Result, error) {
	var res Result
	res.TargetKbps = in.TargetKbps
	digest := Digest(in, cfg)

	init, err := cmaf.ParseInit(in.Init, cmaf.DefaultLimits())
	if err != nil {
		res.Errors = err
		res.Faults = mediaerr.Kinds(err)
		return res, err
	}

	payloads := make(map[int][]byte, len(in.Fragments))
	var collected []error
	var accepted []*cmaf.Fragment
	groupBytes := int64(0)
	for _, fi := range in.Fragments {
		groupBytes += int64(len(fi.Data))
		if groupBytes > cmaf.DefaultLimits().MaxGroupSize {
			collected = append(collected, mediaerr.New(mediaerr.KindInvalidBox, fi.Ordinal, "", 0,
				"capture group exceeds the 2 GiB limit"))
			break
		}
		frag, err := cmaf.ParseFragment(fi.Ordinal, fi.Data, init, cmaf.DefaultLimits())
		if err != nil {
			collected = append(collected, err)
			if mediaerr.Is(err, mediaerr.KindTruncatedBox) {
				res.TruncatedInput = true
			}
			continue
		}
		payloads[fi.Ordinal] = fi.Data
		accepted = append(accepted, frag)
	}

	order := classifyOrder(accepted)
	collected = append(collected, order.errs...)

	wall := make(map[int]int64, len(in.Fragments))
	for _, fi := range in.Fragments {
		wall[fi.Ordinal] = fi.WallNanos
	}
	samples, timeline, clockErrs := buildTrackSamples(accepted, order, wall, init, payloads)
	collected = append(collected, clockErrs...)

	cutter, err := segment.New(segment.Config{
		SegmentNanos:        cfg.SegmentMillis * int64(time.Millisecond),
		BoundarySearchNanos: cfg.SearchMillis * int64(time.Millisecond),
		PreferKeyframes:     true,
		KeyframeSlackNanos:  keyframeSlackNanos,
		MinSegmentNanos:     minSegmentNanos,
	})
	if err != nil {
		res.Errors = errors.Join(append(collected, err)...)
		return res, res.Errors
	}
	man, cutErr := cutter.Cut(toSegmentSamples(samples))
	collected = appendErr(collected, cutErr)
	if vErr := segment.ValidateManifest(man); vErr != nil {
		collected = appendErr(collected, vErr)
	}
	if len(man.Segments) == 0 {
		res.Errors = errors.Join(collected...)
		if res.Errors == nil {
			res.Errors = mediaerr.New(mediaerr.KindNoLegalBoundary, -1, "", 0, "no manifest produced")
		}
		res.Faults = mediaerr.Kinds(res.Errors)
		return res, res.Errors
	}

	silenceRes, silenceBySegment, silErrs := analyseSilence(in.AudioPCM, man, cfg)
	collected = append(collected, silErrs...)

	// RTP timestamps ride alongside the video path: they are unwrapped and
	// any backwards step is reported, so a wrap is never silently treated as
	// a large forward jump.
	rtpStats, rtpErrs := unwrapRTP(in, cfg, order.skip)
	collected = append(collected, rtpErrs...)
	res.WrapCount = rtpStats.wraps
	res.RTPPackets = rtpStats.packets
	res.RTPAnomalies = rtpStats.anomalies

	dedup, fps := deduplicate(man, samples)
	// Two duplicate sources are reported together: fragments that repeat
	// byte-identically in the capture (dropped before cutting) and segments
	// whose content digest matches an already-accepted segment after cutting.
	res.Duplicates = dedup.Duplicates + order.duplicates
	res.Collisions = dedup.Collisions
	collected = append(collected, dedup.errs...)

	entries := buildEntries(man, dedup, fps, silenceBySegment)
	if len(entries) == 0 {
		collected = append(collected, mediaerr.New(mediaerr.KindInvalidBox, -1, "", 0, "dedup removed every segment"))
	}

	// A damaged capture still produces a report and verified blocks, but the
	// staged index is not promoted: publishing it would claim completeness.
	publish := errors.Join(collected...) == nil
	ictx := index.Config{BlockSamples: cfg.BlockSamples, Path: cfg.IndexPath, InputDigest: digest}
	rec, idxErr := writeIndex(ictx, entries, cfg, publish)
	collected = appendErr(collected, idxErr)
	res.RecoveredBlocks = rec.BlocksValid
	res.ResumeOffset = rec.ResumeOffset
	if mediaerr.Is(idxErr, mediaerr.KindResumeMismatch) {
		res.ResumeRefused = true
	}

	res.Entries = entries
	res.Segments = countKind(man.Segments, segment.KindSegment)
	res.QualitySamples = man.Stats.OutputSamples
	res.AlignmentErrorMS = man.Stats.AlignErrorMillis()
	span := man.Stats.TimelineEndNanos - man.Stats.TimelineStartNanos
	res.ActualKbps = actualBitrate(man, span)
	if res.TargetKbps > 0 {
		res.DeviationPct = (res.TargetKbps - res.ActualKbps) / res.TargetKbps * 100
	}
	if md, err := index.ManifestDigest(entries); err == nil {
		res.ManifestSHA256 = fmt.Sprintf("%x", md[:])
	}
	res.DriftPPM = timeline.Model().PPM
	// Silence is measured on the audio track and reported in full: the cut
	// bounds video, while the audio clip is the authoritative silence source.
	res.SilenceMillis = silenceRes.TotalSilenceNanos / int64(time.Millisecond)

	res.Errors = errors.Join(collected...)
	res.Faults = mediaerr.Kinds(res.Errors)
	// Damage that still yielded a manifest is recoverable output: the caller
	// reports it and exits 2 rather than discarding verified work.
	if res.Errors != nil && len(entries) > 0 {
		res.Recovered = true
	}
	return res, res.Errors
}

const (
	minSegmentNanos = 200 * int64(time.Millisecond)
	// keyframeSlackNanos bounds how much alignment error the keyframe
	// preference may cost. It is a fraction of a nominal frame at 30 fps, so
	// a keyframe can win a tie without pushing the boundary far from target.
	keyframeSlackNanos = 20 * int64(time.Millisecond)
)

// orderInfo is the result of fragment ordering classification.
type orderInfo struct {
	errs       []error
	skip       map[int]bool
	repaired   map[int]bool
	wraps      int
	duplicates int
	// rtpAnomalies counts backwards RTP steps beyond tolerance.
	rtpAnomalies int
}

// classifyOrder detects mfhd gaps, repeated or duplicated fragments and tfdt
// regressions. Duplicates are skipped; regressions are marked for repair.
func classifyOrder(frags []*cmaf.Fragment) orderInfo {
	oi := orderInfo{skip: map[int]bool{}, repaired: map[int]bool{}}
	seen := map[[32]byte]int{}
	var lastSeq uint32
	haveSeq := false
	lastTrackID := uint32(0)
	_ = lastTrackID
	for _, f := range frags {
		if prev, isDup := seen[f.Digest]; isDup {
			oi.skip[f.Ordinal] = true
			oi.duplicates++
			oi.errs = append(oi.errs, mediaerr.New(mediaerr.KindDuplicate, f.Ordinal, "moof", f.MoofOffset,
				fmt.Sprintf("fragment is byte-identical to ordinal %d", prev)))
			continue
		}
		seen[f.Digest] = f.Ordinal
		if !haveSeq {
			lastSeq, haveSeq = f.Sequence, true
			continue
		}
		switch {
		case f.Sequence == lastSeq:
			oi.errs = append(oi.errs, mediaerr.New(mediaerr.KindSequenceRepeat, f.Ordinal, "mfhd", f.MoofOffset,
				fmt.Sprintf("sequence number %d repeats the previous fragment", f.Sequence)))
		case f.Sequence > lastSeq+1:
			oi.errs = append(oi.errs, mediaerr.New(mediaerr.KindSequenceGap, f.Ordinal, "mfhd", f.MoofOffset,
				fmt.Sprintf("sequence number jumped from %d to %d (%d fragment(s) missing)",
					lastSeq, f.Sequence, f.Sequence-lastSeq-1)))
		case f.Sequence < lastSeq:
			oi.errs = append(oi.errs, mediaerr.New(mediaerr.KindSequenceRepeat, f.Ordinal, "mfhd", f.MoofOffset,
				fmt.Sprintf("sequence number regressed from %d to %d", lastSeq, f.Sequence)))
		}
		lastSeq = f.Sequence
	}
	return oi
}

// buildTrackSamples places every video sample on the corrected timeline.
//
// Repair runs first, on raw decode times, because a shifted tfdt is also a
// bogus drift observation: feeding it to the fit would tilt the slope and
// corrupt every later correction. Only repaired, monotonic decode times are
// used to fit the sender clock.
func buildTrackSamples(frags []*cmaf.Fragment, oi orderInfo, wall map[int]int64,
	init *cmaf.Init, payloads map[int][]byte) ([]trackSample, *clock.Timeline, []error) {
	var errs []error
	tb := clock.Timebase{Timescale: 90000}
	for _, id := range init.TrackOrder {
		if init.Tracks[id].Handler == "vide" {
			tb = clock.Timebase{Timescale: init.Tracks[id].Timescale}
			break
		}
	}
	timeline := clock.NewTimeline(tb, 0)

	// Pass 1: detect and repair tfdt regressions per track.
	type fragPlan struct {
		frag *cmaf.Fragment
		run  *cmaf.TrackRun
		base int64
	}
	plans := make([]fragPlan, 0, len(frags))
	lastEnd := map[uint32]int64{}
	repairOffset := map[uint32]int64{}
	reported := map[uint32]bool{}
	for _, f := range frags {
		if oi.skip[f.Ordinal] {
			continue
		}
		run := f.VideoRun()
		if run == nil {
			continue
		}
		base := int64(run.BaseDecodeTime) + repairOffset[run.TrackID]
		if base < 0 {
			base = 0
		}
		if end, ok := lastEnd[run.TrackID]; ok && base < end {
			shift := end - base
			repairOffset[run.TrackID] += shift
			if !reported[run.TrackID] {
				reported[run.TrackID] = true
				errs = append(errs, mediaerr.New(mediaerr.KindTfdtRegression, f.Ordinal, "tfdt", f.MoofOffset,
					fmt.Sprintf("track %d base decode time %d regressed below %d; shifted this and later fragments by %d ticks",
						run.TrackID, run.BaseDecodeTime, end, repairOffset[run.TrackID])))
			}
			base = end
		}
		if base != int64(run.BaseDecodeTime) {
			oi.repaired[f.Ordinal] = true
		}
		// The fragment's own samples determine its end on the media clock.
		end := base
		for i := range run.Samples {
			end += int64(run.Samples[i].Duration)
		}
		lastEnd[run.TrackID] = end
		plans = append(plans, fragPlan{frag: f, run: run, base: base})
	}

	// Pass 2: fit drift from repaired decode times only.
	var obs []clock.Observation
	for _, p := range plans {
		if w, ok := wall[p.frag.Ordinal]; ok {
			obs = append(obs, clock.Observation{MediaTicks: uint64(p.base), WallNanos: w})
		}
	}
	if len(obs) > 0 {
		if err := timeline.Fit(obs); err != nil {
			errs = append(errs, err)
		}
	}

	// Pass 3: correct each sample and order the result.
	var points []clock.Corrected
	type meta struct {
		ordinal  int
		index    int
		dur      int64
		size     int
		digest   [32]byte
		keyframe bool
	}
	var metas []meta
	for _, p := range plans {
		cursor := p.base
		data := payloads[p.frag.Ordinal]
		for i := range p.run.Samples {
			s := p.run.Samples[i]
			durNanos, err := tb.TicksToNanos(uint64(s.Duration))
			if err != nil {
				errs = append(errs, mediaerr.Wrap(mediaerr.KindInvalidBox, p.frag.Ordinal, "trun", s.Offset,
					"sample duration overflow", err))
				cursor += int64(s.Duration)
				continue
			}
			c, err := timeline.Correct(uint64(cursor), uint64(s.Duration))
			if err != nil {
				errs = append(errs, err)
				cursor += int64(s.Duration)
				continue
			}
			// Duration must be corrected too: the sample's end is the corrected
			// position of the next decode time. Otherwise drift leaves a tiny
			// hole after every sample and the cutter reports it as a gap.
			endPoint, err := timeline.Correct(uint64(cursor+int64(s.Duration)), uint64(s.Duration))
			if err != nil {
				errs = append(errs, err)
				cursor += int64(s.Duration)
				continue
			}
			durNanos = endPoint.Nanos - c.Nanos
			var digest [32]byte
			if lo, hi := s.Offset, s.Offset+int64(s.Size); lo >= 0 && hi <= int64(len(data)) {
				digest = fingerprint.HashPayload(data[lo:hi])
			}
			points = append(points, c)
			metas = append(metas, meta{
				ordinal:  p.frag.Ordinal,
				index:    i,
				dur:      durNanos,
				size:     int(s.Size),
				digest:   digest,
				keyframe: i == 0,
			})
			cursor += int64(s.Duration)
		}
	}
	if len(points) == 0 {
		return nil, timeline, errs
	}

	ordered, orderErr := timeline.Order(points)
	if orderErr != nil {
		errs = append(errs, orderErr)
	}
	// Order returns a permutation of the input values, so recover which
	// sample each entry refers to. The map is keyed by value, which keeps the
	// permutation stable without exporting clock internals.
	lookup := make(map[clock.Corrected][]int, len(points))
	for i, p := range points {
		lookup[p] = append(lookup[p], i)
	}
	used := make([]bool, len(points))
	out := make([]trackSample, 0, len(points))
	for _, p := range ordered {
		mi := -1
		for _, cand := range lookup[p] {
			if !used[cand] {
				mi = cand
				break
			}
		}
		if mi < 0 {
			continue
		}
		used[mi] = true
		m := metas[mi]
		out = append(out, trackSample{
			ordinal:  m.ordinal,
			index:    m.index,
			start:    p.Nanos,
			dur:      m.dur,
			size:     m.size,
			digest:   m.digest,
			keyframe: m.keyframe,
		})
	}
	return out, timeline, errs
}

func toSegmentSamples(samples []trackSample) []segment.Sample {
	out := make([]segment.Sample, len(samples))
	for i, s := range samples {
		out[i] = segment.Sample{
			Ordinal:  s.ordinal,
			Index:    s.index,
			Nanos:    s.start,
			Duration: s.dur,
			Size:     s.size,
			Keyframe: s.keyframe,
		}
	}
	return out
}

func analyseSilence(pcm audio.PCM, man segment.Manifest, cfg Config) (audio.Result, map[int][]audio.Interval, []error) {
	var errs []error
	silenceCfg := audio.DefaultConfig()
	silenceCfg.WindowNanos = cfg.WindowMillis * int64(time.Millisecond)
	silenceCfg.ThresholdDBFS = cfg.SilenceDBFS
	silenceCfg.MinSilenceNanos = cfg.MinSilenceMillis * int64(time.Millisecond)
	res, err := audio.Detect(pcm, silenceCfg)
	if err != nil {
		return res, nil, []error{err}
	}
	if vErr := audio.Validate(res.Intervals); vErr != nil {
		errs = append(errs, vErr)
	}
	bounds := make([]audio.Interval, 0, len(man.Segments))
	for _, s := range man.Segments {
		bounds = append(bounds, audio.Interval{Start: s.Start, End: s.End})
	}
	bySeg, _ := audio.MapToSegments(res, bounds)
	return res, bySeg, errs
}

type dedupOutcome struct {
	Duplicates int
	Collisions int
	accepted   []bool
	errs       []error
}

// deduplicate runs the fingerprint index over manifest segments, in order.
func deduplicate(man segment.Manifest, samples []trackSample) (dedupOutcome, []fingerprint.Fingerprint) {
	var out dedupOutcome
	fpCfg := fingerprint.DefaultConfig()
	fpCfg.MaxSamplesPerSegment = 1 << 16
	idx, err := fingerprint.NewIndex(fpCfg)
	if err != nil {
		out.errs = append(out.errs, err)
		return out, nil
	}
	var fps []fingerprint.Fingerprint
	var sources []string
	positions := make([]int, 0, len(man.Segments))
	sampleIdx := 0
	for _, seg := range man.Segments {
		if seg.Kind != segment.KindSegment {
			positions = append(positions, -1)
			continue
		}
		var fpSamples []fingerprint.Sample
		for sampleIdx < len(samples) && samples[sampleIdx].start < seg.Start {
			sampleIdx++
		}
		for i := sampleIdx; i < len(samples) && samples[i].start < seg.End; i++ {
			fpSamples = append(fpSamples, fingerprint.Sample{
				Digest:   samples[i].digest,
				Size:     samples[i].size,
				Duration: samples[i].dur,
				Keyframe: samples[i].keyframe,
			})
		}
		fp, err := fpCfg.Compute(fpSamples)
		if err != nil {
			out.errs = append(out.errs, err)
		}
		positions = append(positions, len(fps))
		fps = append(fps, fp)
		sources = append(sources, fmt.Sprintf("segment=%d frags=%v", seg.Index, seg.SourceOrdinals))
	}
	rep := idx.Run(fps, sources)
	out.Duplicates = rep.Duplicates
	out.Collisions = rep.Collisions
	out.accepted = make([]bool, len(man.Segments))
	for i, pos := range positions {
		if pos < 0 {
			continue
		}
		out.accepted[i] = !rep.Decisions[pos].Duplicate
	}
	return out, fps
}

func buildEntries(man segment.Manifest, dd dedupOutcome, fps []fingerprint.Fingerprint,
	silence map[int][]audio.Interval) []index.Entry {
	var entries []index.Entry
	segPos := 0
	for i, seg := range man.Segments {
		if seg.Kind == segment.KindSegment && !dd.accepted[i] {
			continue
		}
		e := index.Entry{
			Kind:        string(seg.Kind),
			StartNanos:  seg.Start,
			EndNanos:    seg.End,
			SampleCount: seg.SampleCount,
			Bytes:       seg.Bytes,
			Source:      seg.SourceOrdinals,
			Reason:      seg.Reason,
		}
		if seg.Kind == segment.KindSegment {
			for _, iv := range silence[i] {
				e.SilenceNanos += iv.Duration()
			}
		}
		if seg.Kind == segment.KindSegment && seg.SampleCount > 0 && segPos < len(fps) {
			e.Fingerprint = fmt.Sprintf("%x", fps[segPos].Content[:16])
			segPos++
		}
		entries = append(entries, e)
	}
	return entries
}

// writeIndex commits the manifest, optionally after simulating a crash and
// recovering the verified prefix. The returned recovery describes the
// checkpoint state that was used to resume.
func writeIndex(cfg index.Config, entries []index.Entry, runCfg Config, publish bool) (index.Recovery, error) {
	// An existing index at this path belongs to a previous run. Validate it
	// before writing anything: if the input changed, appending would silently
	// mix two captures in one manifest.
	if cfg.Path != "" {
		if _, statErr := os.Stat(cfg.Path); statErr == nil {
			existing, openErr := index.Open(cfg.Path, cfg.InputDigest)
			if existing != nil && existing.Recovery.DigestMismatch {
				return existing.Recovery, mediaerr.New(mediaerr.KindResumeMismatch, -1, "index", 0,
					"input digest changed since the index was written; refusing to overwrite")
			}
			if openErr != nil && mediaerr.Is(openErr, mediaerr.KindCorruptCheckpoint) {
				return index.Recovery{}, openErr
			}
		}
	}
	if runCfg.CrashAtBlocks <= 0 || cfg.Path == "" {
		w, err := index.NewWriter(cfg)
		if err != nil {
			return index.Recovery{}, err
		}
		if err := addAll(w, entries); err != nil {
			w.Close()
			return index.Recovery{}, err
		}
		hdr, err := w.Commit()
		if err != nil {
			return index.Recovery{}, err
		}
		if !publish {
			// Keep the verified blocks available under the staging name so
			// recovery can reuse them, but never publish a damaged run.
			if err := os.Rename(cfg.Path, cfg.Path+".partial"); err != nil {
				return index.Recovery{}, err
			}
		}
		return index.Recovery{BlocksValid: int(hdr.Blocks), EntriesFound: len(entries)}, nil
	}

	// Crash path: commit N full blocks into the staging file, leave the
	// published index missing (the process died before the rename), and then
	// append a torn block prefix so even the staging tail needs validation.
	prefix := 0
	for prefix < len(entries) && runCfg.CrashAtBlocks*cfg.BlockSamples > consumed(entries[:prefix]) {
		prefix++
	}
	w, err := index.NewWriter(cfg)
	if err != nil {
		return index.Recovery{}, err
	}
	if err := addAll(w, entries[:prefix]); err != nil {
		w.Close()
		return index.Recovery{}, err
	}
	if _, err := w.Flush(); err != nil {
		w.Close()
		return index.Recovery{}, err
	}
	if err := w.Close(); err != nil {
		return index.Recovery{}, err
	}
	f, err := os.OpenFile(cfg.Path+".tmp", os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return index.Recovery{}, err
	}
	f.Write([]byte{0x41, 0x56, 0x42, 0x4B, 0x7F, 0xFF, 0x00, 0x13, 0x37})
	f.Close()

	r, openErr := index.Open(cfg.Path+".tmp", cfg.InputDigest)
	if r == nil {
		if openErr == nil {
			openErr = errors.New("index: staging checkpoint disappeared during crash simulation")
		}
		return index.Recovery{}, openErr
	}
	// A torn tail is expected here, not a failure of the run.
	var errs []error
	if openErr != nil && !mediaerr.Is(openErr, mediaerr.KindTruncatedIndex) &&
		!mediaerr.Is(openErr, mediaerr.KindChecksumMismatch) {
		errs = append(errs, openErr)
	}
	rw, err := index.ResumeWriter(cfg, r)
	if err != nil {
		return r.Recovery, err
	}
	if err := addAll(rw, entries[len(r.Entries):]); err != nil {
		rw.Close()
		return r.Recovery, err
	}
	if _, err := rw.Commit(); err != nil {
		return r.Recovery, err
	}
	rec := r.Recovery
	rec.EntriesFound = len(entries)
	return rec, errors.Join(errs...)
}

// consumed reports how many media samples a slice of entries accounts for.
func consumed(entries []index.Entry) int {
	n := 0
	for _, e := range entries {
		n += e.SampleCount
	}
	return n
}

func appendErr(dst []error, err error) []error {
	if err == nil {
		return dst
	}
	return append(dst, err)
}

func addAll(w *index.Writer, entries []index.Entry) error {
	for _, e := range entries {
		if _, err := w.Add(e); err != nil {
			return err
		}
	}
	return nil
}

func actualBitrate(man segment.Manifest, spanNanos int64) float64 {
	if spanNanos <= 0 {
		return 0
	}
	seconds := float64(spanNanos) / float64(time.Second)
	return float64(man.Stats.Bytes) * 8 / seconds / 1000
}

func countKind(segs []segment.Segment, kind segment.Kind) int {
	n := 0
	for _, s := range segs {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

type rtpStats struct {
	packets   int
	wraps     int
	anomalies int
}

// unwrapRTP expands every delivered RTP timestamp in delivery order and
// reports wrap crossings and backwards steps. Payload bytes are ignored: the
// media itself comes from the CMAF track.
func unwrapRTP(in Input, cfg Config, skip map[int]bool) (rtpStats, []error) {
	var st rtpStats
	u := clock.NewUnwrapper(cfg.RTPToleranceTicks, cfg.RTPClockRate)
	var errs []error
	for _, f := range in.Fragments {
		// A fragment dropped as a duplicate carries retransmitted packets;
		// replaying them would look like a backwards timestamp step.
		if skip[f.Ordinal] {
			continue
		}
		for _, raw := range f.RTPPackets {
			pkt := &rtp.Packet{}
			if err := pkt.Unmarshal(raw); err != nil {
				errs = append(errs, mediaerr.Wrap(mediaerr.KindInvalidBox, f.Ordinal, "rtp", int64(len(raw)),
					"rtp packet could not be parsed", err))
				continue
			}
			r := u.Push(pkt.Timestamp)
			st.packets++
			if r.Wrapped {
				st.wraps++
			}
			if r.Backwards {
				st.anomalies++
			}
		}
	}
	if err := u.Err(); err != nil {
		errs = append(errs, err)
	}
	return st, errs
}

// SortEntries is a helper for reporting; entries are already in manifest order.
func SortEntries(entries []index.Entry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].StartNanos < entries[j].StartNanos })
}
