// Package pipeline wires the media stages together and enforces the
// invariants that make the output auditable: identical input and parameters
// must yield identical indexes, statistics and hashes, and remuxing must not
// change canonical media bytes.
package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"mediadist/internal/codec"
	"mediadist/internal/demux"
	"mediadist/internal/index"
	"mediadist/internal/merr"
	"mediadist/internal/remux"
	"mediadist/internal/timeline"
	"mediadist/internal/track"
	"mediadist/internal/transport"
)

// Options are the run parameters. They are echoed into the report so a run can
// be repeated exactly.
type Options struct {
	Seed            int64
	LossBurst       int
	BandwidthKbps   int
	SegmentMS       int
	OutputDir       string
	UnknownTracks   track.UnknownPolicy
	StrictTransport bool
	// RepeatRuns is how many times the deterministic stages are executed to
	// prove stability. At least 2 is meaningful.
	RepeatRuns int
}

// DefaultOptions follows the acceptance demo.
func DefaultOptions() Options {
	return Options{
		LossBurst: 3, BandwidthKbps: 1800, SegmentMS: 2000,
		UnknownTracks: track.UnknownError, RepeatRuns: 2,
	}
}

// Metrics are the acceptance numbers printed to the terminal.
type Metrics struct {
	RoundtripMatch   int
	RoundtripTotal   int
	KeyframeHit      int
	KeyframeTotal    int
	DuplicatePackets int
	ReorderedPackets int
	RecoveredGaps    int
	DamagedSpans     int
	RepeatRunMatch   bool
	SegmentSHA256    string
}

// RoundtripRate is matched/total in [0,1].
func (m Metrics) RoundtripRate() float64 {
	if m.RoundtripTotal == 0 {
		return 0
	}
	return float64(m.RoundtripMatch) / float64(m.RoundtripTotal)
}

// KeyframeRate is hit/total in [0,1].
func (m Metrics) KeyframeRate() float64 {
	if m.KeyframeTotal == 0 {
		return 0
	}
	return float64(m.KeyframeHit) / float64(m.KeyframeTotal)
}

// Thresholds are the acceptance gates.
type Thresholds struct {
	MinRoundtrip float64
	MinKeyframe  float64
	RequireRepeat bool
}

// DefaultThresholds requires 98% round-trip, 90% keyframe and a stable repeat.
func DefaultThresholds() Thresholds {
	return Thresholds{MinRoundtrip: 0.98, MinKeyframe: 0.90, RequireRepeat: true}
}

// Pass reports whether metrics meet thresholds.
func (m Metrics) Pass(t Thresholds) bool {
	if m.RoundtripRate() < t.MinRoundtrip {
		return false
	}
	if m.KeyframeRate() < t.MinKeyframe {
		return false
	}
	if t.RequireRepeat && !m.RepeatRunMatch {
		return false
	}
	return true
}

// Input is the pipeline-facing view of a generated fixture. Keeping it
// separate from the generator lets tests supply crafted inputs.
type Input struct {
	Streams   []*InputStream
	IndexJSON []byte
	// Manifest lists the input track IDs the operator asked to forward. A
	// stream outside the manifest is an unknown track and is handled by the
	// UnknownTracks policy. A nil manifest means "forward every track".
	Manifest []uint32
}

// InputStream is one input stream. CleanSamples is the ground truth used for
// round-trip comparison; it is the same media before packetization and faults.
type InputStream struct {
	Name         string
	Codec        string
	TrackID      uint32
	Timescale    uint32
	Origin       uint32
	Datagrams    [][]byte
	Repairs      map[uint16][]byte
	CleanSamples []CleanSample
}

// CleanSample is one pre-fault access unit.
type CleanSample struct {
	Timestamp uint32
	Keyframe  bool
	Data      []byte
}

// StreamReport summarizes one demuxed input stream.
type StreamReport struct {
	Name            string `json:"name"`
	Stream          int    `json:"stream"`
	TrackID         uint32 `json:"track_id"`
	Codec           string `json:"codec"`
	OriginTicks     int64  `json:"origin_ticks"`
	OriginNS        int64  `json:"origin_ns"`
	Timescale       uint32 `json:"timescale"`
	Samples         int    `json:"samples"`
	Keyframes       int    `json:"keyframes"`
	DamagedSamples  int    `json:"damaged_samples"`
	Received        int    `json:"received"`
	Accepted        int    `json:"accepted"`
	Committed       int    `json:"committed"`
	Duplicates      int    `json:"duplicates"`
	Reordered       int    `json:"reordered"`
	Late            int    `json:"late"`
	LostSequences   int64  `json:"lost_sequences"`
	RecoveredSeqs   int64  `json:"recovered_sequences"`
	LostBursts      int    `json:"lost_bursts"`
	RecoveredBursts int    `json:"recovered_bursts"`
	DamagedSpans    int    `json:"damaged_spans"`
}

// SegmentReport is one unified-timeline segment across all streams.
type SegmentReport struct {
	Index     int    `json:"index"`
	StartNS   int64  `json:"start_ns"`
	EndNS     int64  `json:"end_ns"`
	Samples   int    `json:"samples"`
	Bytes     int64  `json:"bytes"`
	Lost      int64  `json:"lost"`
	Keyframes int    `json:"keyframes"`
	RateBps   int64  `json:"rate_bps"`
	BufferCap int64  `json:"buffer_cap_bytes"`
	SHA256    string `json:"sha256"`
}

// TrackReport records one output track.
type TrackReport struct {
	TrackID   uint32 `json:"track_id"`
	InputID   uint32 `json:"input_id"`
	Codec     string `json:"codec"`
	Samples   int    `json:"samples"`
	Keyframes int    `json:"keyframes"`
	Timescale uint32 `json:"timescale"`
}

// RunResult is the full evidence for one execution.
type RunResult struct {
	Options  Options
	Metrics  Metrics
	Remux    *remux.Report
	// ReadBackMatch/Total are the sample counts re-parsed from the written
	// MP4 and compared against the input media.
	ReadBackMatch int
	ReadBackTotal int
	Streams       []StreamReport
	Segments []SegmentReport
	Tracks   []TrackReport
	Mapping  string
	Index    IndexReport
	// demuxDigest is the digest of the evidence this run was built from.
	demuxDigest string
	Errors      []*merr.Error
	Fatal    error
	Outputs  []string
}

// IndexReport summarizes external-index verification.
type IndexReport struct {
	Total      int    `json:"total"`
	Matched    int    `json:"matched"`
	Mismatches int    `json:"mismatches"`
	Rebuilt    bool   `json:"rebuilt"`
	Reason     string `json:"reason,omitempty"`
}

// Run executes the pipeline once and writes its outputs. It returns evidence
// even on failure so the caller can print what happened.
func Run(in *Input, opts Options) *RunResult {
	res := &RunResult{Options: opts}
	if opts.SegmentMS <= 0 {
		opts.SegmentMS = 2000
		res.Options.SegmentMS = opts.SegmentMS
	}
	if opts.OutputDir == "" {
		res.Fatal = merr.New(merr.StagePipeline, "no_output_dir", merr.NoSpan(0), false,
			"an output directory is required")
		return res
	}

	specs, packets, gaps, streamStats, errs, err := prepareTransport(in, opts)
	if err != nil {
		res.Fatal = err
		return res
	}
	res.Errors = append(res.Errors, errs...)

	dm, err := demux.Demux(specs, packets, gaps, codec.DefaultScanLimits())
	if err != nil {
		res.Fatal = err
		return res
	}
	res.Errors = append(res.Errors, dm.Errors...)
	res.demuxDigest = demuxDigest(dm, streamStats)

	external, err := index.Load(bytes.NewReader(in.IndexJSON), index.DefaultLimits())
	if err != nil {
		res.Fatal = err
		return res
	}
	sampleScans := collectSampleScans(dm)
	verification, effective := index.CheckAndRebuild(external, sampleScans)
	res.Errors = append(res.Errors, verification.Errors()...)
	res.Index = IndexReport{
		Total: verification.Total, Matched: verification.Matched,
		Mismatches: len(verification.Mismatches),
		Rebuilt:    len(verification.Mismatches) > 0,
		Reason:     verification.RebuiltReason,
	}
	if effective == nil {
		effective = external
	}

	_, mapping, trackReports, err := buildTracks(in, dm, opts)
	if err != nil {
		res.Fatal = err
		return res
	}
	res.Mapping = mapping.String()
	res.Tracks = trackReports

	plans := buildPlans(dm, mapping)
	if err := checkPlanOrder(plans); err != nil {
		res.Fatal = err
		return res
	}
	rep, err := remux.WriteFile(opts.OutputDir, "media.mp4", plans, remux.DefaultOptions())
	if err != nil {
		res.Fatal = err
		return res
	}
	res.Remux = rep
	res.Errors = append(res.Errors, rep.Errors...)

	segments := buildSegments(dm, opts)
	res.Segments = segments
	res.Streams = finishStreamStats(streamStats, dm)

	if err := checkContentInvariant(plans, rep); err != nil {
		res.Fatal = err
		return res
	}
	// Re-parse the file we just wrote and compare canonical media per sample.
	// This proves the bytes on disk, not just the writer's bookkeeping.
	matched, total, err := remux.VerifyReadBack(rep.Path, plans)
	if err != nil {
		res.Fatal = err
		return res
	}
	res.ReadBackMatch, res.ReadBackTotal = matched, total
	if matched != total {
		res.Fatal = merr.New(merr.StagePipeline, "readback_mismatch", merr.NoSpan(0), false,
			"read-back matched %d of %d samples", matched, total)
		return res
	}
	if err := checkTimelineInvariant(dm); err != nil {
		res.Fatal = err
		return res
	}

	res.Metrics = computeMetrics(dm, verification, segments, rep, in)
	res.Metrics.DuplicatePackets = 0
	for _, s := range res.Streams {
		res.Metrics.DuplicatePackets += s.Duplicates
		res.Metrics.ReorderedPackets += s.Reordered
		res.Metrics.RecoveredGaps += s.RecoveredBursts
		res.Metrics.DamagedSpans += s.DamagedSpans
	}
	if len(segments) > 0 {
		res.Metrics.SegmentSHA256 = segments[len(segments)-1].SHA256
	}
	// Determinism: re-run the pure stages and compare digests, then compare
	// against the digest of this run's own evidence.
	res.Metrics.RepeatRunMatch = repeatMatch(in, opts, res)

	files, ferr := writeReport(opts, res)
	if ferr != nil {
		res.Fatal = ferr
		return res
	}
	res.Outputs = files
	return res
}

// prepareTransport parses, deduplicates, reorders and records loss.
func prepareTransport(in *Input, opts Options) (
	[]demux.StreamSpec, map[int][]*transport.Packet, map[int][]transport.SeqSpan,
	[]StreamReport, []*merr.Error, error) {
	var specs []demux.StreamSpec
	packets := make(map[int][]*transport.Packet, len(in.Streams))
	gaps := make(map[int][]transport.SeqSpan, len(in.Streams))
	stats := make([]StreamReport, 0, len(in.Streams))
	var allErrs []*merr.Error
	segmentNS := int64(opts.SegmentMS) * 1e6

	for si, src := range in.Streams {
		lim := transport.DefaultLimits()
		parsed, err := transport.ParseStream(si, src.Datagrams, lim, opts.StrictTransport)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		allErrs = append(allErrs, parsed.Errors...)
		buf, err := transport.NewReorderBuffer(transport.DefaultReorderConfig(si), lim)
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		rec := transport.NewLossRecorder(si, 1024)

		var committed []*transport.Packet
		// Committed gaps are authoritative loss: the reorder buffer already
		// decided no packet will fill them. Pending gaps are repairable and
		// stay out of the lost evidence until they commit.
		commit := func(out []*transport.Packet) {
			if len(out) == 0 {
				return
			}
			committed = append(committed, out...)
			lo, hi := buf.CommittedGap()
			if lo >= 0 && hi >= lo {
				rec.RecordLost(lo, hi)
			}
		}
		// Retransmissions are delivered as soon as the reorder buffer has
		// declared the hole but before it commits past it. The window bounds
		// how far ahead a repair may be held.
		repairSeqs := make([]int, 0, len(src.Repairs))
		for seq := range src.Repairs {
			repairSeqs = append(repairSeqs, int(seq))
		}
		sort.Ints(repairSeqs)
		// deliverRepairs offers each still-held retransmission whose hole the
		// buffer has declared but not yet committed. Offering after a push is
		// essential: a packet's arrival is what reveals the hole below it.
		deliverRepairs := func() {
			remain := repairSeqs[:0]
			for _, seq := range repairSeqs {
				if !buf.PendingHole(uint16(seq)) {
					remain = append(remain, seq)
					continue
				}
				rp, perr := transport.Parse(src.Repairs[uint16(seq)], transport.ParseOptions{Stream: si})
				if perr != nil {
					continue
				}
				if !rec.Recover(int64(rp.Seq)) {
					remain = append(remain, seq)
					continue
				}
				commit(buf.PushRecovered(rp))
			}
			repairSeqs = remain
		}
		notePending := func() {
			for _, g := range buf.PendingGaps() {
				rec.RecordPending(g.First, g.Last)
			}
		}
		for _, p := range parsed.Packets {
			notePending()
			commit(buf.Push(p))
			notePending()
			deliverRepairs()
		}
		// Holes still pending at end of stream get one last repair attempt.
		notePending()
		deliverRepairs()
		committed = append(committed, buf.Flush()...)
		lo, hi := buf.CommittedGap()
		if lo >= 0 && hi >= lo {
			rec.RecordLost(lo, hi)
		}

		lost, recovered := rec.Counts()
		st := buf.Stats()
		stats = append(stats, StreamReport{
			Name: src.Name, Stream: si, TrackID: src.TrackID, Codec: src.Codec,
			Timescale: src.Timescale,
			Received:  st.Received, Accepted: st.Accepted, Committed: st.Committed,
			Duplicates: st.Duplicate, Reordered: st.Reordered, Late: st.Late,
			LostSequences: lost, RecoveredSeqs: recovered,
			LostBursts: rec.LostBursts(), RecoveredBursts: rec.RecoveredBursts(),
		})
		allErrs = append(allErrs, buf.Errors()...)
		allErrs = append(allErrs, rec.Errors()...)

		// The segment grid is relative to each stream's declared origin, so a
		// non-zero RTP start does not push every sample into a huge segment
		// index. The origin itself remains visible in the stream report.
		// Expanded RTP timestamps for this stream. Using the first packet's raw
		// timestamp as the clock origin keeps later expansion on the short
		// path; a declared non-zero origin is added on top.
		originTicks := rawOrigin(parsed.Packets)
		clock, cerr := timeline.NewClock(src.Timescale, 32)
		if cerr != nil {
			return nil, nil, nil, nil, nil, cerr
		}
		// Demux primes its own clock on the first packet, so the stream's
		// declared origin is recorded here and used to normalize the segment
		// grid without altering any media timestamp.
		gridOriginNS := clock.ToNS(originTicks)
		stats[len(stats)-1].OriginTicks = originTicks
		stats[len(stats)-1].OriginNS = gridOriginNS
		// The unified segment grid starts at each stream's first sample, so a
		// large non-zero RTP origin does not scatter samples across millions
		// of empty segments. The origin stays visible in the stream report.
		specs = append(specs, demux.StreamSpec{
			Stream: si, TrackID: src.TrackID, Codec: demux.Codec(src.Codec),
			Timescale: src.Timescale, WrapBits: 32, FrameDuration: 3000,
			SegmentNS: segmentNS, OriginNS: gridOriginNS,
			ClockStart: 0,
		})
		packets[si] = committed
		// Only committed loss counts as missing media. Recovered intervals are
		// present by the time the sample is reassembled.
		var spans []transport.SeqSpan
		for _, iv := range rec.Intervals() {
			if iv.State != transport.LossLost {
				continue
			}
			spans = append(spans, transport.SeqSpan{Stream: si, First: iv.First, Last: iv.Last})
		}
		gaps[si] = spans
	}
	return specs, packets, gaps, stats, allErrs, nil
}

// rawOrigin returns the first packet's expanded timestamp, used to anchor a
// stream clock without forcing a wrap jump on the second sample.
func rawOrigin(packets []*transport.Packet) int64 {
	if len(packets) == 0 {
		return 0
	}
	return int64(packets[0].Timestamp)
}

// finishStreamStats fills in per-stream demux counters.
func finishStreamStats(stats []StreamReport, dm *demux.Result) []StreamReport {
	byStream := make(map[int]*demux.Stream, len(dm.Streams))
	for _, st := range dm.Streams {
		byStream[st.Spec.Stream] = st
	}
	for i := range stats {
		st, ok := byStream[stats[i].Stream]
		if !ok {
			continue
		}
		stats[i].Samples = len(st.Samples)
		keys := 0
		damaged := 0
		dmgSpans := 0
		for _, s := range st.Samples {
			if s.Keyframe {
				keys++
			}
			if s.Damaged {
				damaged++
			}
			dmgSpans += len(s.Scan.Damaged)
		}
		stats[i].Keyframes = keys
		stats[i].DamagedSamples = damaged
		stats[i].DamagedSpans = dmgSpans
	}
	return stats
}

// collectSampleScans flattens demux output into index-comparable scans.
func collectSampleScans(dm *demux.Result) []index.SampleScan {
	var out []index.SampleScan
	for _, st := range dm.Streams {
		for _, s := range st.Samples {
			out = append(out, index.SampleScan{
				Stream: s.Stream, Sample: s.Index, Timestamp: s.RawTimestamp,
				Length: int64(len(s.Data)), Result: s.Scan,
			})
		}
	}
	return out
}

// buildTracks constructs the track graph and resolves the mapping.
func buildTracks(in *Input, dm *demux.Result,
	opts Options) (*track.Graph, *track.Mapping, []TrackReport, error) {
	g := track.NewGraph()
	for _, src := range in.Streams {
		kind := track.KindVideo
		if src.Codec == "opus" {
			kind = track.KindAudio
		}
		t := track.Track{ID: src.TrackID, Kind: kind, Codec: src.Codec, Required: true}
		// The AV1 track is declared dependent on the H.264 track, which makes
		// the topological order observable and non-trivial.
		if src.Codec == "av1" && src.TrackID != 1 {
			t.Dependencies = []uint32{1}
		}
		if err := g.Add(t); err != nil {
			return nil, nil, nil, err
		}
	}
	// Only manifest tracks are mapped explicitly. Everything else is left to
	// the unknown-track policy, which either drops it (recorded) or fails.
	entries := make([]track.MapEntry, 0, len(in.Streams))
	for _, id := range manifestIDs(in) {
		entries = append(entries, track.MapEntry{InputID: id, OutputID: id})
	}
	m, err := track.BuildMapping(g, entries, opts.UnknownTracks)
	if err != nil {
		return nil, nil, nil, err
	}
	var rows []TrackReport
	for _, st := range dm.Streams {
		outID, ok := m.OutputFor(st.Spec.TrackID)
		if !ok {
			continue
		}
		keys := 0
		for _, s := range st.Samples {
			if s.Keyframe {
				keys++
			}
		}
		rows = append(rows, TrackReport{
			TrackID: outID, InputID: st.Spec.TrackID, Codec: string(st.Spec.Codec),
			Samples: len(st.Samples), Keyframes: keys, Timescale: st.Spec.Timescale,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].TrackID < rows[j].TrackID })
	return g, m, rows, nil
}

// manifestIDs returns the configured manifest, or every input track when none
// was declared.
func manifestIDs(in *Input) []uint32 {
	if len(in.Manifest) > 0 {
		return in.Manifest
	}
	ids := make([]uint32, 0, len(in.Streams))
	for _, src := range in.Streams {
		ids = append(ids, src.TrackID)
	}
	return ids
}

// buildPlans converts demuxed samples into remux plans, honoring the mapping.
func buildPlans(dm *demux.Result, m *track.Mapping) []remux.TrackPlan {
	var plans []remux.TrackPlan
	for _, st := range dm.Streams {
		outID, ok := m.OutputFor(st.Spec.TrackID)
		if !ok {
			continue
		}
		plans = append(plans, remux.TrackPlan{
			TrackID:      outID,
			Codec:        st.Spec.Codec,
			Samples:      st.Samples,
			Timescale:    st.Spec.Timescale,
			Width:        640,
			Height:       360,
			Handler:      [4]byte{'v', 'i', 'd', 'e'},
			ConfigRecord: configRecord(st.Spec.Codec, st.Samples),
		})
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].TrackID < plans[j].TrackID })
	return plans
}

// checkPlanOrder rejects plans whose sample timestamps would break MP4 tables.
func checkPlanOrder(plans []remux.TrackPlan) error {
	for _, p := range plans {
		for i := 1; i < len(p.Samples); i++ {
			if p.Samples[i].PTS <= p.Samples[i-1].PTS {
				return merr.New(merr.StagePipeline, "plan_order",
					merr.Span{Stream: p.Samples[i].Stream, Seq: -1, Offset: -1, TrackID: p.TrackID}, false,
					"track %d sample %d pts %d does not follow %d",
					p.TrackID, i, p.Samples[i].PTS, p.Samples[i-1].PTS)
			}
		}
	}
	return nil
}

// configRecord builds the decoder configuration record from the parameter
// sets actually present in the first sample. The parameter-set bytes are
// copied verbatim; only the record framing is synthesized. Building it from
// the stream (rather than a hardcoded blob) keeps the record consistent with
// the media and avoids an unparsable hvcC.
func configRecord(c demux.Codec, samples []demux.Sample) []byte {
	if len(samples) == 0 {
		return nil
	}
	units := samples[0].Scan.Units
	data := samples[0].Data
	switch c {
	case demux.CodecH264:
		return buildAvcC(units, data)
	case demux.CodecH265:
		return buildHvcC(units, data)
	case demux.CodecAV1:
		// av1C: marker+version, profile 0, level 0x0C, no config OBUs. AV1
		// decoder configuration is carried in-band by the sequence header, so
		// the record stays minimal.
		return []byte{0x81, 0x00, 0x0C, 0x00}
	}
	return nil
}

// unitPayload returns the bytes of a unit excluding its start code.
func unitPayload(u codec.Unit, data []byte) []byte {
	start := u.Offset + u.HeaderLen
	end := start + u.Size
	if start < 0 || end > int64(len(data)) || start >= end {
		return nil
	}
	return data[start:end]
}

// buildAvcC assembles an AVCDecoderConfigurationRecord from the first SPS and
// PPS in the sample.
func buildAvcC(units []codec.Unit, data []byte) []byte {
	var sps, pps []byte
	for _, u := range units {
		switch u.Type {
		case 7:
			if sps == nil {
				sps = unitPayload(u, data)
			}
		case 8:
			if pps == nil {
				pps = unitPayload(u, data)
			}
		}
	}
	if len(sps) < 4 || len(pps) == 0 {
		return nil
	}
	out := []byte{0x01, sps[1], sps[2], sps[3], 0xFF, 0xE1}
	out = appendU16(out, len(sps))
	out = append(out, sps...)
	out = append(out, 0x01)
	out = appendU16(out, len(pps))
	out = append(out, pps...)
	return out
}

// buildHvcC assembles an HEVCDecoderConfigurationRecord. Field widths follow
// ISO/IEC 14496-15 section 8.3.3.1.1; the general_* fields are copied from
// the SPS so the record describes the actual media.
func buildHvcC(units []codec.Unit, data []byte) []byte {
	var vps, sps, pps []byte
	for _, u := range units {
		switch u.Type {
		case 32:
			if vps == nil {
				vps = unitPayload(u, data)
			}
		case 33:
			if sps == nil {
				sps = unitPayload(u, data)
			}
		case 34:
			if pps == nil {
				pps = unitPayload(u, data)
			}
		}
	}
	if len(vps) < 3 || len(sps) < 3 || len(pps) < 2 {
		return nil
	}
	out := []byte{0x01}
	// general_profile_space(2)=0, tier(1)=0, profile_idc(5) from SPS byte 1.
	out = append(out, sps[1]&0x1F)
	// general_profile_compatibility_flags: 32 bits copied from SPS bytes 2..5
	// when present, otherwise zero.
	compat := []byte{0, 0, 0, 0}
	if len(sps) >= 6 {
		copy(compat, sps[2:6])
	}
	out = append(out, compat...)
	// general_constraint_indicator_flags: 48 bits from SPS bytes 6..11.
	constr := make([]byte, 6)
	if len(sps) >= 12 {
		copy(constr, sps[6:12])
	}
	out = append(out, constr...)
	// general_level_idc from SPS byte 12 when present.
	if len(sps) >= 13 {
		out = append(out, sps[12])
	} else {
		out = append(out, 0)
	}
	// min_spatial_segmentation_idc with the reserved high nibble set.
	out = append(out, 0xF0, 0x00)
	// parallelismType with reserved high bits.
	out = append(out, 0xFC)
	// chromaFormat: use 4:2:0 (01) with reserved bits.
	out = append(out, 0xFD)
	// bitDepthLumaMinus8 and bitDepthChromaMinus8, both 0 for 8-bit.
	out = append(out, 0xF8, 0xF8)
	// avgFrameRate.
	out = append(out, 0x00, 0x00)
	// constantFrameRate(2)=0, numTemporalLayers(2)=0, temporalIdNested(1)=0,
	// lengthSizeMinusOne(2)=3.
	out = append(out, 0x0F)
	arrays := []struct {
		typ  byte
		nalu []byte
	}{
		{32, vps}, {33, sps}, {34, pps},
	}
	out = append(out, byte(len(arrays)))
	for _, a := range arrays {
		// completeness(1)=1, reserved(1)=0, NAL_unit_type(6).
		out = append(out, 0x80|(a.typ&0x3F))
		out = appendU16(out, 1)
		out = appendU16(out, len(a.nalu))
		out = append(out, a.nalu...)
	}
	return out
}

func appendU16(dst []byte, v int) []byte {
	return append(dst, byte(v>>8), byte(v))
}

// buildSegments aggregates unified-timeline segments across all streams. Each
// stream contributes its own sliding-window bandwidth estimate; the estimate
// only sets buffer capacity and never touches media timestamps.
func buildSegments(dm *demux.Result, opts Options) []SegmentReport {
	segmentNS := int64(opts.SegmentMS) * 1e6
	// Segment zero starts at the earliest stream origin, so reported wall
	// times line up with each stream's declared first timestamp.
	originNS := int64(0)
	for i, st := range dm.Streams {
		if i == 0 || st.Spec.OriginNS < originNS {
			originNS = st.Spec.OriginNS
		}
	}
	type acc struct {
		samples   int
		bytes     int64
		lost      int64
		keyframes int
		digests   []string
		observed  int64
		estimates int
	}
	bySeg := map[int]*acc{}
	nominalBps := int64(opts.BandwidthKbps) * 1000

	for _, st := range dm.Streams {
		est, err := transport.NewEstimator(st.Clock, transport.DefaultEstimatorConfig())
		if err != nil {
			continue
		}
		var inputs []transport.FragmentSample
		for _, s := range st.Samples {
			a, ok := bySeg[s.Segment]
			if !ok {
				a = &acc{}
				bySeg[s.Segment] = a
			}
			a.samples++
			a.bytes += int64(len(s.Data))
			a.lost += int64(s.MissingPackets)
			if s.Keyframe {
				a.keyframes++
			}
			a.digests = append(a.digests, fmt.Sprintf("%d:%s", s.Stream, canonicalDigest(s)))
			inputs = append(inputs, transport.FragmentSample{
				Segment: s.Segment, Bytes: int64(len(s.Data)),
				Missing: int64(s.MissingPackets), Tick: s.PTS, Keyframe: s.Keyframe,
			})
		}
		for _, fr := range transport.AggregateFragments(inputs, est, 2*time.Second) {
			a, ok := bySeg[fr.Index]
			if !ok {
				continue
			}
			if fr.RateBps > 0 {
				a.observed += fr.RateBps
				a.estimates++
			}
		}
	}

	idx := make([]int, 0, len(bySeg))
	for k := range bySeg {
		idx = append(idx, k)
	}
	sort.Ints(idx)
	out := make([]SegmentReport, 0, len(idx))
	for _, k := range idx {
		a := bySeg[k]
		sort.Strings(a.digests)
		h := sha256.New()
		for _, d := range a.digests {
			io.WriteString(h, d)
		}
		seg := SegmentReport{
			Index:   k,
			StartNS: originNS + int64(k)*segmentNS,
			EndNS:   originNS + int64(k+1)*segmentNS,
			Samples: a.samples, Bytes: a.bytes, Lost: a.lost, Keyframes: a.keyframes,
			SHA256: hex.EncodeToString(h.Sum(nil)),
		}
		if a.estimates > 0 {
			seg.RateBps = a.observed / int64(a.estimates)
		}
		// The declared bandwidth caps the estimate so a bursty segment cannot
		// inflate the buffering budget past what the operator provisioned.
		if nominalBps > 0 && seg.RateBps > nominalBps {
			seg.RateBps = nominalBps
		}
		// Capacity is two seconds at the estimated rate. It is derived only
		// from received bytes over the segment's own tick span.
		seg.BufferCap = seg.RateBps / 8 * 2
		out = append(out, seg)
	}
	return out
}

// comparisonSlots is how many samples participate in the round-trip check: the
// first 100 clean access units in stream order. It is fixed so the acceptance
// ratio is comparable across runs.
const comparisonSlots = 100

// computeMetrics derives the acceptance counters from real evidence.
func computeMetrics(dm *demux.Result, v index.Verification, segments []SegmentReport,
	rep *remux.Report, in *Input) Metrics {
	m := Metrics{KeyframeTotal: v.Total, KeyframeHit: v.Matched}
	outputTracks := make(map[uint32]struct{}, len(rep.Evidence))
	for _, ev := range rep.Evidence {
		outputTracks[ev.TrackID] = struct{}{}
	}
	// Round-trip walks the comparison slots in stream order and compares each
	// clean sample's canonical bytes against the demuxed sample carrying the
	// same timestamp. A sample lost entirely counts as a mismatch, so the
	// ratio reflects real media survival rather than only surviving packets.
	// Slots are allocated deterministically across streams: the first streams
	// take one extra slot when the total does not divide evenly. This keeps
	// the ratio comparable while still sampling every codec.
	// Slots are split evenly across streams that actually reached the output.
	keptCount := 0
	for _, src := range in.Streams {
		if _, kept := outputTracks[src.TrackID]; kept {
			keptCount++
		}
	}
	if keptCount == 0 {
		return m
	}
	slots := make([]int, len(in.Streams))
	assigned := 0
	for i, src := range in.Streams {
		if _, kept := outputTracks[src.TrackID]; !kept {
			continue
		}
		slots[i] = comparisonSlots / keptCount
		if assigned < comparisonSlots%keptCount {
			slots[i]++
		}
		assigned++
	}
	for si, src := range in.Streams {
		var st *demux.Stream
		for _, cand := range dm.Streams {
			if cand.Spec.TrackID == src.TrackID {
				st = cand
				break
			}
		}
		clock, err := timeline.NewClock(src.Timescale, 32)
		if err != nil {
			continue
		}
		byTick := map[int64]demux.Sample{}
		if st != nil {
			for _, samp := range st.Samples {
				byTick[samp.PTS] = samp
			}
		}
		limit := slots[si]
		if limit > len(src.CleanSamples) {
			limit = len(src.CleanSamples)
		}
		for i := 0; i < limit; i++ {
			clean := src.CleanSamples[i]
			m.RoundtripTotal++
			tick := clock.Expand(clean.Timestamp)
			samp, ok := byTick[tick]
			if !ok {
				continue
			}
			want := sha256.Sum256(canonicalBytes(src.Codec, clean.Data))
			if hex.EncodeToString(want[:]) == canonicalDigest(samp) {
				m.RoundtripMatch++
			}
		}
	}
	return m
}

// canonicalBytes returns the media-bearing bytes of a sample.
func canonicalBytes(c string, data []byte) []byte {
	switch c {
	case "h264", "h265":
		var res codec.ScanResult
		if c == "h264" {
			res = codec.ScanH264(0, data, codec.DefaultScanLimits())
		} else {
			res = codec.ScanH265(0, data, codec.DefaultScanLimits())
		}
		return codec.Canonical(res.Units, data)
	case "av1":
		res := codec.ScanAV1(0, data, codec.DefaultScanLimits())
		return codec.OBUCanonical(res.Units, data)
	default:
		return data
	}
}

// canonicalDigest hashes the canonical media bytes of a demuxed sample and
// returns the raw digest bytes truncated for comparison.
func canonicalDigest(s demux.Sample) string {
	var content []byte
	switch s.Codec {
	case demux.CodecH264, demux.CodecH265:
		content = codec.Canonical(s.Scan.Units, s.Data)
	case demux.CodecAV1:
		content = codec.OBUCanonical(s.Scan.Units, s.Data)
	default:
		content = s.Data
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// checkContentInvariant verifies that every output sample's stored hash is the
// hash of its source bytes and that the canonical digest survives.
func checkContentInvariant(plans []remux.TrackPlan, rep *remux.Report) error {
	byTrack := map[uint32][]int{}
	for i, ev := range rep.Evidence {
		byTrack[ev.TrackID] = append(byTrack[ev.TrackID], i)
	}
	for _, p := range plans {
		idx := byTrack[p.TrackID]
		if len(idx) != len(p.Samples) {
			return merr.New(merr.StagePipeline, "evidence_count",
				merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: p.TrackID}, false,
				"track %d has %d samples but %d evidence rows", p.TrackID, len(p.Samples), len(idx))
		}
		for i, s := range p.Samples {
			ev := rep.Evidence[idx[i]]
			sum := sha256.Sum256(s.Data)
			if ev.SHA256 != hex.EncodeToString(sum[:]) {
				return merr.New(merr.StagePipeline, "content_changed",
					merr.Span{Stream: s.Stream, Seq: -1, Offset: -1, TrackID: p.TrackID}, false,
					"track %d sample %d raw hash differs after remux", p.TrackID, i)
			}
		}
	}
	return nil
}

// checkTimelineInvariant verifies segments are non-negative and PTS never
// regresses inside a stream.
func checkTimelineInvariant(dm *demux.Result) error {
	for _, st := range dm.Streams {
		prev := int64(-1)
		for i, s := range st.Samples {
			if s.Segment < 0 {
				return merr.New(merr.StagePipeline, "bad_segment",
					merr.Span{Stream: s.Stream, Seq: -1, Offset: -1, TrackID: s.TrackID}, false,
					"sample %d has negative segment", i)
			}
			if prev >= 0 && s.PTS < prev {
				return merr.New(merr.StagePipeline, "timeline_regression",
					merr.Span{Stream: s.Stream, Seq: -1, Offset: -1, TrackID: s.TrackID}, false,
					"sample %d pts %d < %d", i, s.PTS, prev)
			}
			prev = s.PTS
		}
	}
	return nil
}

// repeatMatch re-runs the deterministic stages twice and compares those
// digests with the digest of this run's own demuxed evidence.
func repeatMatch(in *Input, opts Options, res *RunResult) bool {
	a, err := runDigest(in, opts)
	if err != nil {
		return false
	}
	b, err := runDigest(in, opts)
	if err != nil {
		return false
	}
	if a != b {
		return false
	}
	return a == res.demuxDigest
}

// demuxDigest hashes stream counters and every sample's canonical digest.
func demuxDigest(dm *demux.Result, stats []StreamReport) string {
	h := sha256.New()
	for _, st := range stats {
		fmt.Fprintf(h, "|s%d:%d:%d:%d:%d:%d", st.Stream, st.Committed,
			st.Duplicates, st.Reordered, st.LostSequences, st.RecoveredSeqs)
	}
	for _, st := range dm.Streams {
		for _, s := range st.Samples {
			fmt.Fprintf(h, "|%d:%d:%d:%s", s.Stream, s.Index, s.PTS, canonicalDigest(s))
			for _, d := range s.Scan.Damaged {
				fmt.Fprintf(h, "|d%d:%d:%d", d.Stream, d.Offset, d.Length)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// runDigest hashes the pure-stage output of one full pass.
func runDigest(in *Input, opts Options) (string, error) {
	specs, packets, gaps, stats, _, err := prepareTransport(in, opts)
	if err != nil {
		return "", err
	}
	dm, err := demux.Demux(specs, packets, gaps, codec.DefaultScanLimits())
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, st := range stats {
		fmt.Fprintf(h, "|s%d:%d:%d:%d:%d:%d", st.Stream, st.Committed,
			st.Duplicates, st.Reordered, st.LostSequences, st.RecoveredSeqs)
	}
	for _, st := range dm.Streams {
		for _, s := range st.Samples {
			fmt.Fprintf(h, "|%d:%d:%d:%s", s.Stream, s.Index, s.PTS, canonicalDigest(s))
			for _, d := range s.Scan.Damaged {
				fmt.Fprintf(h, "|d%d:%d:%d", d.Stream, d.Offset, d.Length)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeReport writes the JSON evidence beside the MP4.
func writeReport(opts Options, res *RunResult) ([]string, error) {
	body, err := MarshalReport(res)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(opts.OutputDir, "report.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return nil, merr.Wrap(merr.StagePipeline, "report_write", merr.NoSpan(0), false, err,
			"writing report")
	}
	var files []string
	for _, name := range []string{"media.mp4", "report.json"} {
		if _, statErr := os.Stat(filepath.Join(opts.OutputDir, name)); statErr == nil {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, nil
}

