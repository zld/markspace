package pipeline

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"sort"

	"mediaforge/internal/audio"
	"mediaforge/internal/clock"
	"mediaforge/internal/manifest"
	"mediaforge/internal/mediaerr"
	"mediaforge/internal/mux"
	"mediaforge/internal/packet"
	"mediaforge/internal/quality"
	"mediaforge/internal/timeline"
)

// packetData is one unique packet placed on the source clock.
type packetData struct {
	seq     uint16
	extSeq  int64
	arrival int
	// start and frames are absolute RTP ticks, not output samples.
	start   int64
	frames  int64
	status  manifest.SegmentStatus
	late    bool
	payload []byte
}

// converter turns a parsed capture into one audio timeline.
type converter struct {
	opts   Options
	stream *packet.Stream
	mgr    *timeline.Manager
	// statuses keeps the classification of each unique packet keyed by its
	// extended sequence number so decode/replay errors survive into the
	// manifest.
	statuses map[int64]manifest.SegmentStatus
	// payloadBytes is the total RTP payload actually received, the basis for
	// the input bitrate.
	payloadBytes int64
}

func newConverter(opts Options, stream *packet.Stream) *converter {
	return &converter{opts: opts, stream: stream,
		// Watermark delay is expressed in RTP clock ticks.
		mgr:      timeline.NewManager(opts.ClockRate, int64(opts.DelayMs)*opts.ClockRate/1000),
		statuses: make(map[int64]manifest.SegmentStatus)}
}

// collect feeds every RTP record to the replay manager and validates RTCP.
func (c *converter) collect(stats *quality.Stats) {
	bytesPerFrame := int64(c.opts.BitsPerSample / 8)
	for _, rec := range c.stream.Records {
		if rec.Kind == packet.KindRTCP {
			if rec.RTCP == nil {
				stats.BadPackets++
				stats.BadDetails = append(stats.BadDetails, badRecord(rec, "bad_rtcp"))
				continue
			}
			if _, err := packet.ParseReports(rec.RTCP, rec.Offset); err != nil {
				stats.BadPackets++
				stats.BadDetails = append(stats.BadDetails, badRecord(rec, mediaerr.CodeOf(err)))
			}
			continue
		}
		if rec.Packet == nil {
			stats.BadPackets++
			code := mediaerr.CodeOf(rec.Err)
			switch code {
			case mediaerr.CodeTruncated:
				stats.TruncatedPackets++
			case mediaerr.CodeCorrupt:
				stats.CorruptPayloads++
			}
			stats.BadDetails = append(stats.BadDetails, badRecord(rec, code))
			continue
		}
		p := rec.Packet
		stats.Packets++
		c.payloadBytes += int64(len(p.Payload))
		status := manifest.StatusOK
		switch mediaerr.CodeOf(rec.Err) {
		case mediaerr.CodeCorrupt:
			stats.CorruptPayloads++
			stats.BadPackets++
			stats.BadDetails = append(stats.BadDetails, badRecord(rec, mediaerr.CodeCorrupt))
			status = manifest.StatusCorrupt
		case mediaerr.CodeTruncated:
			stats.TruncatedPackets++
			stats.BadPackets++
			stats.BadDetails = append(stats.BadDetails, badRecord(rec, mediaerr.CodeTruncated))
			status = manifest.StatusTruncated
		}
		declared := int64(len(p.Payload))
		if status == manifest.StatusTruncated && int64(rec.DeclaredLen) > int64(p.HeaderLen) {
			declared = int64(rec.DeclaredLen) - int64(p.HeaderLen)
		}
		frames := declared / bytesPerFrame / int64(c.opts.PayloadLayout)
		if frames == 0 {
			frames = int64(c.opts.ClockRate) * 20 / 1000 // assume 20 ms
		}
		ev := c.mgr.Push(p.Seq, p.Timestamp, p.Payload, frames)
		if ev.Duplicate {
			continue
		}
		c.statuses[ev.ExtSeq] = status
		if ev.Late {
			stats.LateReplays++
		}
	}
}

// layout maps every unique packet to an absolute RTP tick position. The
// expansion is seeded from the lowest sequence number so a reordered file and
// the same file in order produce identical positions.
func (c *converter) layout(sum timeline.Summary) []packetData {
	events := append([]timeline.Event(nil), sum.Events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].ExtSeq < events[j].ExtSeq })
	out := make([]packetData, 0, len(events))
	if len(events) == 0 {
		return out
	}
	u := &clock.Unwrapper32{}
	var base int64
	for i, ev := range events {
		tick := u.Push(ev.Timestamp)
		if i == 0 {
			base = tick
		}
		status := manifest.StatusOK
		if s, ok := c.statuses[ev.ExtSeq]; ok {
			status = s
		}
		if ev.Late {
			status = manifest.StatusReplayed
		}
		out = append(out, packetData{seq: ev.Seq, extSeq: ev.ExtSeq, arrival: ev.Arrival,
			start: tick - base, frames: ev.Samples, status: status, late: ev.Late,
			payload: ev.Payload})
	}
	return out
}

// decode converts one payload's 16-bit PCM to the output channel layout while
// staying on the source sample clock.
func (c *converter) decode(pd packetData) []float64 {
	srcFrames := pd.frames
	if srcFrames < 0 {
		srcFrames = 0
	}
	if pd.status == manifest.StatusCorrupt || int64(len(pd.payload)) < 2*srcFrames*int64(c.opts.PayloadLayout) {
		// Corrupt or truncated: keep the timeline duration but emit silence,
		// so downstream timing stays aligned with the sender's clock.
		return make([]float64, srcFrames*int64(c.opts.Channels))
	}
	src := make([]float64, srcFrames*int64(c.opts.PayloadLayout))
	for i := range src {
		lo := int16(pd.payload[2*i]) | int16(pd.payload[2*i+1])<<8
		src[i] = float64(lo) / 32768
	}
	if c.opts.PayloadLayout == c.opts.Channels {
		return src
	}
	target := audio.Layout{Name: audio.LayoutMono, Channels: c.opts.Channels}
	if c.opts.Channels == 2 {
		target.Name = audio.LayoutStereo
	}
	out, err := audio.Normalize(src, c.opts.PayloadLayout, target)
	if err != nil {
		return nil
	}
	return out
}

// render writes the source-clock buffer in arrival order. Late packets
// overwrite their interval, which is the deterministic replay; the affected
// ranges are returned merged.
func (c *converter) render(parts []packetData) ([]float64, []quality.Range, int64) {
	total := int64(0)
	for _, p := range parts {
		if end := p.start + p.frames; end > total {
			total = end
		}
	}
	buf := make([]float64, total*int64(c.opts.Channels))
	byArrival := append([]packetData(nil), parts...)
	sort.SliceStable(byArrival, func(i, j int) bool { return byArrival[i].arrival < byArrival[j].arrival })
	var replay []quality.Range
	for _, p := range byArrival {
		pcm := c.decode(p)
		if p.late {
			replay = append(replay, quality.Range{Start: p.start, End: p.start + p.frames})
		}
		copyAt(buf, pcm, p.start, int64(c.opts.Channels))
	}
	return buf, mergeRanges(replay), total
}

// copyAt overwrites [start,start+len(pcm)/ch) and zero-pads a short payload.
func copyAt(buf, pcm []float64, start, channels int64) {
	off := start * channels
	if off < 0 || off >= int64(len(buf)) {
		return
	}
	n := copy(buf[off:], pcm)
	for i := off + int64(n); i < off+int64(len(pcm)) && i < int64(len(buf)); i++ {
		buf[i] = 0
	}
}

func mergeRanges(in []quality.Range) []quality.Range {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Start < in[j].Start })
	out := []quality.Range{in[0]}
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// segments builds manifest events on the source clock. Holes between packets
// are silent intervals; a late packet's interval is marked replayed.
func (c *converter) segments(parts []packetData, total int64) []manifest.Event {
	events := make([]manifest.Event, 0, len(parts))
	sorted := append([]packetData(nil), parts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].start == sorted[j].start {
			return sorted[i].extSeq < sorted[j].extSeq
		}
		return sorted[i].start < sorted[j].start
	})
	cursor := int64(0)
	for _, p := range sorted {
		if p.start > cursor {
			events = append(events, manifest.Event{Status: manifest.StatusMissing,
				StartSample: cursor, EndSample: p.start})
			cursor = p.start
		}
		end := p.start + p.frames
		if end <= cursor {
			continue
		}
		start := p.start
		if start < cursor {
			start = cursor
		}
		events = append(events, manifest.Event{Status: p.status, Seq: p.seq,
			StartSample: start, EndSample: end})
		cursor = end
	}
	if cursor < total {
		events = append(events, manifest.Event{Status: manifest.StatusMissing,
			StartSample: cursor, EndSample: total})
	}
	return events
}

// finish resamples once, applies loudness control, then muxes and writes the
// manifest and statistics.
func (c *converter) finish(res Result, stats quality.Stats) (quality.Stats, error) {
	c.collect(&stats)
	sum := c.mgr.Summary()
	stats.UniquePackets = int64(sum.Unique)
	stats.Duplicates = int64(sum.Duplicates)
	stats.Loss = sum.Loss

	parts := c.layout(sum)
	if len(parts) == 0 {
		return stats, mediaerr.New(mediaerr.CodeUnrecoverable, "no decodable RTP packets")
	}
	src, replayRanges, srcFrames := c.render(parts)
	if srcFrames == 0 {
		return stats, mediaerr.New(mediaerr.CodeUnrecoverable, "no audio frames")
	}
	events := c.segments(parts, srcFrames)

	var mix []float64
	if int64(c.opts.SampleRate) == c.opts.ClockRate {
		mix = src
	} else {
		r := audio.NewResampler(c.opts.ClockRate, c.opts.SampleRate, int(c.opts.Channels))
		mix = r.Process(src, len(src)/int(c.opts.Channels))
		mix = append(mix, r.Flush()...)
	}
	before := audio.Measure(mix, int(c.opts.Channels), int(c.opts.SampleRate))
	out, gain := audio.ApplyGain(mix, int(c.opts.Channels), int(c.opts.SampleRate),
		c.opts.TargetLUFS, c.opts.CeilingDBFS)
	outFrames := int64(len(out) / int(c.opts.Channels))
	if outFrames == 0 {
		return stats, mediaerr.New(mediaerr.CodeUnrecoverable, "processed audio is empty")
	}

	// Manifest and missing ranges use output samples; both are derived from
	// the same rational map as the resampler's frame count.
	scale := func(v int64) int64 {
		return clock.MulDivRound(v, c.opts.SampleRate, c.opts.ClockRate)
	}
	for i := range events {
		events[i].StartSample = scale(events[i].StartSample)
		events[i].EndSample = scale(events[i].EndSample)
	}
	missing := make([]mux.Range, 0, len(replayRanges)+len(events))
	for _, r := range replayRanges {
		missing = append(missing, mux.Range{Start: scale(r.Start), End: scale(r.End)})
	}
	var manifestMissing []manifest.Range
	for _, ev := range events {
		if ev.Status == manifest.StatusOK {
			continue
		}
		r := mux.Range{Start: ev.StartSample, End: ev.EndSample}
		missing = append(missing, r)
		manifestMissing = append(manifestMissing, manifest.Range{StartSample: ev.StartSample, EndSample: ev.EndSample})
	}
	info, err := mux.Write(mux.Spec{
		Path: res.MP4Path, Rate: int(c.opts.SampleRate), Channels: int(c.opts.Channels),
		PCM: toPCM16(out), ChunkFrames: chunkPlan(outFrames, c.opts.SampleRate),
		Missing: missing,
	})
	if err != nil {
		return stats, err
	}
	hash, err := quality.HashFile(res.MP4Path)
	if err != nil {
		return stats, err
	}
	tl := manifest.Timeline{
		SampleRate: int(c.opts.SampleRate), Channels: int(c.opts.Channels),
		RTPClockRate: c.opts.ClockRate, DurationSample: outFrames,
		MediaFile: baseName(res.MP4Path), MediaSHA256: hash,
		Segments: manifest.MakeSegments(int(c.opts.SampleRate), events),
		Missing:  manifestMissing,
	}
	if err := manifest.Write(res.ManifestPath, tl); err != nil {
		return stats, err
	}

	stats.InputLUFS = before.IntegratedLUFS
	stats.OutputLUFS = gain.After.IntegratedLUFS
	stats.PeakDBFS = gain.After.PeakDBFS
	stats.ClippedSamples = int64(gain.ClippedAfter)
	stats.OutputSamples = outFrames
	stats.ManifestSamples = tl.DurationSample
	stats.ManifestAlignmentSample = quality.AlignmentSamples(tl.DurationSample, info.Frames)
	stats.OutputBytes = info.DataBytes
	stats.OutputDurationSecs = info.DurationS
	stats.IndexComplete = info.IndexComplete
	stats.OutputSHA256 = hash
	// Target bitrate is the source format's lossless payload rate; the input
	// bitrate is the payload bytes actually received over the same duration,
	// so framing overhead and lost packets are reflected honestly.
	stats.TargetBitrateBps = float64(c.opts.ClockRate * int64(c.opts.PayloadLayout) * int64(c.opts.BitsPerSample))
	if info.DurationS > 0 {
		stats.InputBitrateBps = float64(c.payloadBytes*8) / info.DurationS
	}
	stats.BitrateErrorPct = quality.BitrateError(stats.InputBitrateBps, stats.TargetBitrateBps)
	if err := writeStats(res.StatsPath, stats); err != nil {
		return stats, err
	}
	return stats, nil
}

// badRecord captures the locating context of a rejected record.
func badRecord(rec packet.Record, code mediaerr.Code) quality.BadRecord {
	out := quality.BadRecord{Offset: rec.Offset, Code: string(code)}
	if rec.Packet != nil {
		out.Seq = rec.Packet.Seq
	}
	var me *mediaerr.Error
	if errors.As(rec.Err, &me) {
		if me.HasSeq {
			out.Seq = me.Seq
		}
		out.Message = me.Error()
	} else if rec.Err != nil {
		out.Message = rec.Err.Error()
	}
	return out
}

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func toPCM16(samples []float64) []int16 {
	out := make([]int16, len(samples))
	for i, v := range samples {
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		out[i] = int16(math.Round(v * 32767))
	}
	return out
}

func chunkPlan(frames, rate int64) []int {
	if frames <= 0 {
		return []int{1}
	}
	chunk := rate / 100
	if chunk < 1 {
		chunk = 1
	}
	out := make([]int, 0, frames/chunk+1)
	for frames > 0 {
		n := chunk
		if n > frames {
			n = frames
		}
		out = append(out, int(n))
		frames -= n
	}
	return out
}

func writeStats(path string, s quality.Stats) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return mediaerr.New(mediaerr.CodeIO, "marshal stats: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return mediaerr.New(mediaerr.CodeIO, "write stats: %v", err)
	}
	return nil
}
