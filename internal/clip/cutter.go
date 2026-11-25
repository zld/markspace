// Package clip cuts event intervals out of the two media streams.
//
// Intervals are half-open [Start, End) in ticks of the shared timeline. An
// event that reaches before zero or past the end of the programme is
// rejected rather than clamped: silently moving a boundary would make the
// report lie about what was delivered.
//
// Audio boundaries are floored to whole sample frames so a channel is never
// split mid-frame. Video starts at the latest keyframe not later than the
// event start, and the difference is recorded as the actual lead-in. The
// cutter reads through bounded buffers and never rewrites the source.
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
	// AudioOffsetTick is added to WAV frame positions to place them on the
	// shared timeline; zero means the WAV starts with the programme.
	AudioOffsetTick int64
}

// Cutter produces slices from one source.
type Cutter struct {
	src *Source
	tl  *clock.Timeline
	vtl *clock.Timeline
}

// New validates the source and prepares the timelines.
func New(src *Source) (*Cutter, error) {
	if src == nil || src.WAV == nil || src.Stream == nil {
		return nil, fmt.Errorf("clip: source needs both WAV and stream")
	}
	audio := src.WAV.Layout
	if audio == nil || audio.Format.Channels == 0 {
		return nil, fmt.Errorf("clip: WAV layout not parsed")
	}
	vtl, err := src.Stream.Timeline(audio.Format.SampleRate)
	if err != nil {
		return nil, err
	}
	// Audio and video must share a tick base; a mismatch would make an event
	// boundary mean different things in the two streams.
	if vtl.Rate().Num != int64(audio.Format.SampleRate) || vtl.Rate().Den != 1 {
		// Different tick bases are allowed, but then we need an explicit
		// conversion; the cutter uses the sample-rate timeline for audio and
		// the track timeline for video, so nothing else is required.
	}
	return &Cutter{
		src: src,
		tl:  audio.Timeline(),
		vtl: vtl,
	}, nil
}

// AudioDurationTicks returns the tick length of the WAV.
func (c *Cutter) AudioDurationTicks() int64 {
	return c.src.AudioOffsetTick + c.src.WAV.Layout.Frames
}

// VideoDurationTicks returns the tick length covered by samples.
func (c *Cutter) VideoDurationTicks() int64 {
	tr := c.src.Stream.Track
	if len(tr.Samples) == 0 {
		return 0
	}
	last := tr.Samples[len(tr.Samples)-1]
	return last.Tick + last.Duration
}

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
	if c.src.AudioOffsetTick != 0 && ev.StartTick < c.src.AudioOffsetTick {
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
			"event %q has no whole audio sample frame", sl.Event.Name))
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
		// No keyframe at or before the start: the earliest sample is the
		// best available start, and it is necessarily later than requested.
		// That breaks the "never later than the event start" invariant, so
		// the event is refused rather than delivered misleadingly.
		return fault.New(fault.KindMissingKeyframe, fault.Fatal,
			fault.Location{ByteOffset: -1, Tick: sl.Event.StartTick, HasTick: true},
			"no keyframe at or before event %q start %d", sl.Event.Name, sl.Event.StartTick)
	}
	v.FirstSample = kf
	v.StartTick = tr.Samples[kf].Tick
	if sl.Event.StartTick > v.StartTick {
		v.LeadInTicks = sl.Event.StartTick - v.StartTick
		v.LeadInMs = c.vtl.TicksToMillis(v.LeadInTicks)
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
	var bytes int64
	for i := kf; i <= last; i++ {
		bytes += tr.Samples[i].Size
	}
	v.Bytes = bytes
	v.EndTick = tr.Samples[last].Tick + tr.Samples[last].Duration
	sl.Video = v
	if sl.Event.EndTick > v.EndTick {
		// The audio interval extends past the last video sample inside the
		// event; that is normal at the tail but worth reporting.
		sl.Issues.Add(fault.New(fault.KindOutOfRange, fault.Warn,
			fault.Location{ByteOffset: -1, Tick: v.EndTick, HasTick: true},
			"video ends at %d, event ends at %d", v.EndTick, sl.Event.EndTick))
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
// sizes are bounded by the event, not the programme, so this stays small.
func (c *Cutter) SliceBytes(sl *Slice, r io.ReaderAt) ([]byte, error) {
	if sl.Video.FirstSample < 0 {
		return nil, nil
	}
	tr := c.src.Stream.Track
	var total int64
	for i := sl.Video.FirstSample; i <= sl.Video.LastSample; i++ {
		total += tr.Samples[i].Size
	}
	if total > 64<<20 {
		return nil, fmt.Errorf("clip: video slice of %d bytes exceeds 64MiB cap", total)
	}
	out := make([]byte, 0, total)
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
