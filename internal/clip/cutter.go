// Package clip cuts event intervals out of the two media streams.
//
// Every event interval is half-open [Start, End) in ticks of one shared
// tick base (the video track timescale). Audio frame positions are mapped
// into that base through the PCM sample rate; video sample decode times are
// already in it. An event that starts before zero or ends past either
// stream is rejected rather than clamped: silently moving a boundary would
// make the report lie about what was delivered.
//
// Audio boundaries are floored to whole sample frames so a channel is never
// split mid-frame. Video starts at the latest keyframe not later than the
// event start, and the difference is recorded as the actual lead-in.
package clip

import (
	"fmt"
	"io"

	"replaycheck/internal/clock"
	"replaycheck/internal/fault"
	"replaycheck/internal/stream"
	"replaycheck/internal/wav"
)

// Event is a half-open tick interval to cut.
type Event struct {
	Name      string `json:"name"`
	StartTick int64  `json:"start_tick"`
	EndTick   int64  `json:"end_tick"`
}

// AudioCut describes the audio slice.
type AudioCut struct {
	StartFrame int64 `json:"start_frame"`
	EndFrame   int64 `json:"end_frame"`
	Frames     int64 `json:"frames"`
	Bytes      int64 `json:"bytes"`
	// StartTick/EndTick are the tick instants of the sample-aligned edges.
	StartTick int64 `json:"start_tick"`
	EndTick   int64 `json:"end_tick"`
}

// VideoCut describes the video slice.
type VideoCut struct {
	FirstSample int   `json:"first_sample"`
	LastSample  int   `json:"last_sample"` // inclusive; -1 when empty
	Samples     int   `json:"samples"`
	Bytes       int64 `json:"bytes"`
	StartTick   int64 `json:"start_tick"`
	EndTick     int64 `json:"end_tick"`
	// LeadInTicks is how far before the event start the video begins
	// because the boundary was moved back to a keyframe.
	LeadInTicks int64 `json:"lead_in_ticks"`
	LeadInMs    int64 `json:"lead_in_ms"`
	// EndSlackTicks is how far the last video sample ends before the event
	// end; the audio cut covers the full interval regardless.
	EndSlackTicks int64 `json:"end_slack_ticks"`
}

// Slice is one event's cut.
type Slice struct {
	Event  Event      `json:"event"`
	Audio  AudioCut   `json:"audio"`
	Video  VideoCut   `json:"video"`
	Issues fault.List `json:"-"`
}

// Source bundles the inputs a cutter needs.
type Source struct {
	WAV    *wav.Reader
	Stream *stream.Info
	// AudioOffsetTick is where the WAV starts on the shared tick base.
	AudioOffsetTick int64
	// TickRate is the shared tick base; it must equal the video track
	// timescale. Zero is rejected: callers must be explicit.
	TickRate clock.Rate
}

// Cutter produces slices from one source.
type Cutter struct {
	src *Source
	tl  *clock.Timeline
}

// New validates the source and prepares the shared timeline.
func New(src *Source) (*Cutter, error) {
	if src == nil || src.WAV == nil || src.Stream == nil {
		return nil, fmt.Errorf("clip: source needs both WAV and stream")
	}
	lay := src.WAV.Layout
	if lay == nil || lay.Format.Channels == 0 || lay.Format.SampleRate == 0 {
		return nil, fmt.Errorf("clip: WAV layout not parsed")
	}
	if src.Stream.Track == nil {
		return nil, fmt.Errorf("clip: stream has no track")
	}
	trackRate := clock.Rate{Num: int64(src.Stream.Track.Timescale), Den: 1}
	tickRate := src.TickRate
	if tickRate.Num == 0 {
		tickRate = trackRate
	}
	if tickRate != trackRate {
		return nil, fmt.Errorf("clip: shared tick rate %s does not match video timescale %s",
			tickRate, trackRate)
	}
	tl, err := clock.New(tickRate, lay.Format.SampleRate)
	if err != nil {
		return nil, err
	}
	return &Cutter{src: src, tl: tl}, nil
}

// AudioDurationTicks returns the shared-tick length of the WAV.
func (c *Cutter) AudioDurationTicks() int64 {
	return c.src.AudioOffsetTick + c.tl.SamplesToTicks(c.src.WAV.Layout.Frames)
}

// VideoDurationTicks returns the shared-tick length covered by the samples.
func (c *Cutter) VideoDurationTicks() int64 {
	tr := c.src.Stream.Track
	if len(tr.Samples) == 0 {
		return 0
	}
	last := tr.Samples[len(tr.Samples)-1]
	return last.Tick + last.Duration
}

// TickRate returns the shared tick base.
func (c *Cutter) TickRate() clock.Rate { return c.tl.Rate() }

// Cut validates ev and returns the slice geometry. The returned slice may
// still carry issues for regions that overlap known damage; the caller
// decides whether to deliver it.
func (c *Cutter) Cut(ev Event) (*Slice, error) {
	if ev.EndTick <= ev.StartTick {
		return nil, fault.New(fault.KindOutOfRange, fault.Fatal,
			fault.Location{ByteOffset: -1, Tick: ev.StartTick, HasTick: true},
			"event %q has empty or reversed interval [%d,%d)", ev.Name, ev.StartTick, ev.EndTick)
	}
	audioDur := c.AudioDurationTicks()
	videoDur := c.VideoDurationTicks()
	if ev.StartTick < 0 || ev.EndTick > audioDur || ev.EndTick > videoDur {
		limit := audioDur
		if videoDur < limit {
			limit = videoDur
		}
		return nil, fault.New(fault.KindOutOfRange, fault.Fatal,
			fault.Location{ByteOffset: -1, Tick: ev.EndTick, HasTick: true},
			"event %q [%d,%d) exceeds programme length %d ticks", ev.Name, ev.StartTick, ev.EndTick, limit)
	}
	if ev.StartTick < c.src.AudioOffsetTick {
		return nil, fault.New(fault.KindOutOfRange, fault.Fatal,
			fault.Location{ByteOffset: -1, Tick: ev.StartTick, HasTick: true},
			"event %q starts before audio origin %d", ev.Name, c.src.AudioOffsetTick)
	}

	sl := &Slice{Event: ev}
	c.cutAudio(sl)
	if err := c.cutVideo(sl); err != nil {
		return nil, err
	}
	return sl, nil
}

func (c *Cutter) cutAudio(sl *Slice) {
	base := c.src.AudioOffsetTick
	startFrame := c.tl.AlignDownSamples(sl.Event.StartTick - base)
	endFrame := c.tl.AlignDownSamples(sl.Event.EndTick - base)
	maxFrames := c.src.WAV.Layout.Frames
	if endFrame > maxFrames {
		endFrame = maxFrames
	}
	if startFrame < 0 {
		startFrame = 0
	}
	audio := AudioCut{
		StartFrame: startFrame,
		EndFrame:   endFrame,
		StartTick:  base + c.tl.SamplesToTicks(startFrame),
		EndTick:    base + c.tl.SamplesToTicks(endFrame),
	}
	if endFrame > startFrame {
		audio.Frames = endFrame - startFrame
		audio.Bytes = audio.Frames * int64(c.src.WAV.Layout.Format.BlockAlign)
	}
	sl.Audio = audio
	if audio.Frames == 0 {
		sl.Issues.Add(fault.New(fault.KindOutOfRange, fault.Damaged,
			fault.Location{ByteOffset: -1, Tick: sl.Event.StartTick, HasTick: true},
			"event %q contains no whole audio sample frame", sl.Event.Name))
	}
}

func (c *Cutter) cutVideo(sl *Slice) error {
	tr := c.src.Stream.Track
	v := VideoCut{FirstSample: -1, LastSample: -1}
	if len(tr.Samples) == 0 {
		sl.Video = v
		sl.Issues.Add(fault.New(fault.KindMissingKeyframe, fault.Damaged,
			fault.ByteAt(0), "no video samples in stream"))
		return nil
	}
	kf, ok := tr.KeyframeAtOrBefore(sl.Event.StartTick)
	if !ok {
		// Refuse rather than start late: a fragment that begins after the
		// requested instant would misrepresent the event boundary.
		return fault.New(fault.KindMissingKeyframe, fault.Fatal,
			fault.Location{ByteOffset: -1, Tick: sl.Event.StartTick, HasTick: true},
			"no keyframe at or before event %q start %d", sl.Event.Name, sl.Event.StartTick)
	}
	v.FirstSample = kf
	v.StartTick = tr.Samples[kf].Tick
	if sl.Event.StartTick > v.StartTick {
		v.LeadInTicks = sl.Event.StartTick - v.StartTick
		v.LeadInMs = c.tl.TicksToMillis(v.LeadInTicks)
	}
	last := kf
	for i := kf + 1; i < len(tr.Samples); i++ {
		if tr.Samples[i].Tick >= sl.Event.EndTick {
			break
		}
		last = i
	}
	v.LastSample = last
	v.Samples = last - kf + 1
	for i := kf; i <= last; i++ {
		v.Bytes += tr.Samples[i].Size
	}
	v.EndTick = tr.Samples[last].Tick + tr.Samples[last].Duration
	if sl.Event.EndTick > v.EndTick {
		v.EndSlackTicks = sl.Event.EndTick - v.EndTick
	}
	sl.Video = v
	if sl.Event.EndTick > v.EndTick {
		sl.Issues.Add(fault.New(fault.KindOutOfRange, fault.Warn,
			fault.Location{ByteOffset: -1, Tick: v.EndTick, HasTick: true},
			"video coverage ends at %d, event ends at %d", v.EndTick, sl.Event.EndTick))
	}
	return nil
}

// CopyAudio writes the slice's audio frames to dst in bounded blocks.
func (c *Cutter) CopyAudio(sl *Slice, dst io.Writer) (int64, error) {
	const blockFrames = 4096
	remaining := sl.Audio.Frames
	start := sl.Audio.StartFrame
	var written int64
	for remaining > 0 {
		n := int64(blockFrames)
		if n > remaining {
			n = remaining
		}
		buf, err := c.src.WAV.ReadFrames(start, n)
		if err != nil {
			return written, err
		}
		w, err := dst.Write(buf)
		written += int64(w)
		if err != nil {
			return written, err
		}
		start += n
		remaining -= n
	}
	return written, nil
}

// SliceBytes reads the video sample payloads of a slice into memory. Clip
// size is bounded by the event, not the programme, so this stays small.
func (c *Cutter) SliceBytes(sl *Slice, r io.ReaderAt) ([]byte, error) {
	if sl.Video.FirstSample < 0 {
		return nil, nil
	}
	tr := c.src.Stream.Track
	if sl.Video.Bytes > 64<<20 {
		return nil, fmt.Errorf("clip: video slice of %d bytes exceeds 64MiB cap", sl.Video.Bytes)
	}
	out := make([]byte, 0, sl.Video.Bytes)
	for i := sl.Video.FirstSample; i <= sl.Video.LastSample; i++ {
		s := tr.Samples[i]
		buf := make([]byte, s.Size)
		if _, err := r.ReadAt(buf, s.Offset); err != nil {
			return nil, err
		}
		out = append(out, buf...)
	}
	return out, nil
}
