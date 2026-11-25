package clip

import (
	"bytes"
	"encoding/binary"
	"testing"

	"replaycheck/internal/clock"
	"replaycheck/internal/fault"
	"replaycheck/internal/stream"
	"replaycheck/internal/wav"
)

// fixture builds a 48 kHz stereo WAV and a 90 kHz video track whose
// keyframes land every keyEvery video samples (30 fps).
type fixture struct {
	mp4Img []byte
	wr     *wav.Reader
	info   *stream.Info
	cutter *Cutter
}

func newFixture(t *testing.T, seconds int, keyEvery int) *fixture {
	t.Helper()
	const rate = 48000
	frames := seconds * rate
	pcm := make([]byte, frames*2*2)
	for i := 0; i < frames; i++ {
		v := int16((i * 7) % 20000)
		binary.LittleEndian.PutUint16(pcm[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(pcm[i*4+2:], uint16(v/2))
	}
	wavImg := wav.BuildPCM(2, rate, 16, pcm)
	wr, err := wav.NewReader(bytes.NewReader(wavImg), int64(len(wavImg)))
	if err != nil {
		t.Fatal(err)
	}
	fps := 30
	mp4Img, err := stream.Build(stream.Config{
		Timescale: 90000, FPS: fps, Width: 32, Height: 24,
		SampleCount: seconds * fps, KeyEvery: keyEvery, SampleSize: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.ParseFile(bytes.NewReader(mp4Img), "v.mp4", int64(len(mp4Img)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(&Source{
		WAV:      wr,
		Stream:   info,
		TickRate: clock.MustRate(90000, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{mp4Img: mp4Img, wr: wr, info: info, cutter: c}
}

func TestSharedTickBaseRequired(t *testing.T) {
	f := newFixture(t, 2, 30)
	// A 1 kHz base cannot represent 90 kHz video ticks faithfully.
	_, err := New(&Source{WAV: f.wr, Stream: f.info, TickRate: clock.MustRate(1000, 1)})
	if err == nil {
		t.Fatal("mismatched tick base should be refused")
	}
}

func TestAudioBoundarySampleAligned(t *testing.T) {
	f := newFixture(t, 4, 30)
	// 90001 ticks is 48000.53 samples at 48 kHz: the boundary must floor to
	// sample 48000 and never split a frame.
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 90001, EndTick: 180000})
	if err != nil {
		t.Fatal(err)
	}
	if sl.Audio.StartFrame != 48000 {
		t.Fatalf("start frame = %d, want 48000", sl.Audio.StartFrame)
	}
	if sl.Audio.StartTick != 90000 {
		t.Fatalf("start tick = %d, want 90000", sl.Audio.StartTick)
	}
	if sl.Audio.Frames != 48000 {
		t.Fatalf("frames = %d, want 48000", sl.Audio.Frames)
	}
	if sl.Audio.Bytes != sl.Audio.Frames*4 {
		t.Fatalf("bytes = %d", sl.Audio.Bytes)
	}
}

func TestMultiChannelFrameAlignment(t *testing.T) {
	// 44.1 kHz stereo at 90 kHz: one frame is 2.0408 ticks, so only
	// frame-aligned boundaries survive.
	const rate = 44100
	pcm := make([]byte, rate*2*2)
	wavImg := wav.BuildPCM(2, rate, 16, pcm)
	wr, err := wav.NewReader(bytes.NewReader(wavImg), int64(len(wavImg)))
	if err != nil {
		t.Fatal(err)
	}
	mp4Img, err := stream.Build(stream.Config{Timescale: 90000, FPS: 30, SampleCount: 30})
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.ParseFile(bytes.NewReader(mp4Img), "v.mp4", int64(len(mp4Img)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(&Source{WAV: wr, Stream: info, TickRate: clock.MustRate(90000, 1)})
	if err != nil {
		t.Fatal(err)
	}
	sl, err := c.Cut(Event{Name: "e", StartTick: 45000, EndTick: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if sl.Audio.Bytes%int64(wr.Layout.Format.BlockAlign) != 0 {
		t.Fatalf("audio bytes %d not a multiple of block align %d", sl.Audio.Bytes, wr.Layout.Format.BlockAlign)
	}
	if sl.Audio.Frames != sl.Audio.EndFrame-sl.Audio.StartFrame {
		t.Fatal("frame count mismatch")
	}
}

func TestVideoStartsAtKeyframeNotAfter(t *testing.T) {
	f := newFixture(t, 4, 30) // keyframes at 0s, 1s, 2s, 3s
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 135000, EndTick: 180000})
	if err != nil {
		t.Fatal(err)
	}
	if sl.Video.StartTick > sl.Event.StartTick {
		t.Fatalf("video starts at %d, after event start %d", sl.Video.StartTick, sl.Event.StartTick)
	}
	if sl.Video.StartTick != 90000 {
		t.Fatalf("video start = %d, want 90000", sl.Video.StartTick)
	}
	if sl.Video.LeadInTicks != 45000 {
		t.Fatalf("lead-in = %d, want 45000", sl.Video.LeadInTicks)
	}
	if sl.Video.LeadInMs != 500 {
		t.Fatalf("lead-in ms = %d, want 500", sl.Video.LeadInMs)
	}
	for i := sl.Video.FirstSample; i <= sl.Video.LastSample; i++ {
		if f.info.Track.Samples[i].Tick >= 180000 {
			t.Fatalf("sample %d at %d is at/after event end", i, f.info.Track.Samples[i].Tick)
		}
	}
	if !f.info.Track.Samples[sl.Video.FirstSample].Key {
		t.Fatal("first video sample is not a keyframe")
	}
}

func TestEventOutOfRangeRejected(t *testing.T) {
	f := newFixture(t, 2, 30)
	if _, err := f.cutter.Cut(Event{Name: "x", StartTick: 0, EndTick: 90000 * 3}); !fault.IsKind(err, fault.KindOutOfRange) {
		t.Fatalf("err = %v, want out-of-range", err)
	}
	if _, err := f.cutter.Cut(Event{Name: "x", StartTick: -10, EndTick: 100}); !fault.IsKind(err, fault.KindOutOfRange) {
		t.Fatalf("negative start err = %v, want out-of-range", err)
	}
	if _, err := f.cutter.Cut(Event{Name: "x", StartTick: 100, EndTick: 100}); !fault.IsKind(err, fault.KindOutOfRange) {
		t.Fatalf("empty interval err = %v, want out-of-range", err)
	}
}

func TestLeftClosedRightOpen(t *testing.T) {
	f := newFixture(t, 2, 30)
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 90000, EndTick: 180000})
	if err != nil {
		t.Fatal(err)
	}
	if sl.Audio.StartFrame != 48000 || sl.Audio.EndFrame != 96000 {
		t.Fatalf("audio [%d,%d), want [48000,96000)", sl.Audio.StartFrame, sl.Audio.EndFrame)
	}
	for i := sl.Video.FirstSample; i <= sl.Video.LastSample; i++ {
		if f.info.Track.Samples[i].Tick == 180000 {
			t.Fatalf("video included sample exactly at the open end")
		}
	}
	// The sample that starts exactly at the end must not be in the slice.
	if sl.Video.EndTick > 180000 {
		t.Fatalf("video end tick %d crosses the closed-open boundary", sl.Video.EndTick)
	}
}

func TestCopyAudioLength(t *testing.T) {
	f := newFixture(t, 2, 30)
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 0, EndTick: 90000})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := f.cutter.CopyAudio(sl, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != sl.Audio.Bytes || int64(buf.Len()) != sl.Audio.Bytes {
		t.Fatalf("copied %d bytes, want %d", n, sl.Audio.Bytes)
	}
	if int64(buf.Len()) != 48000*4 {
		t.Fatalf("audio length = %d, want 192000", buf.Len())
	}
}

func TestVideoSliceBytesMatchSamples(t *testing.T) {
	f := newFixture(t, 2, 30)
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 0, EndTick: 90000})
	if err != nil {
		t.Fatal(err)
	}
	data, err := f.cutter.SliceBytes(sl, bytes.NewReader(f.mp4Img))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != sl.Video.Bytes {
		t.Fatalf("slice bytes = %d, want %d", len(data), sl.Video.Bytes)
	}
	if len(data) == 0 {
		t.Fatal("expected video payload")
	}
}

func TestNoKeyframeBeforeStartRefused(t *testing.T) {
	const rate = 48000
	wavImg := wav.BuildPCM(2, rate, 16, make([]byte, rate*2*2))
	wr, _ := wav.NewReader(bytes.NewReader(wavImg), int64(len(wavImg)))
	mp4Img, err := stream.Build(stream.Config{
		Timescale: 90000, FPS: 30, SampleCount: 30, Width: 32, Height: 24,
		Keyframes: []int{15}, SampleSize: 40,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.ParseFile(bytes.NewReader(mp4Img), "v.mp4", int64(len(mp4Img)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(&Source{WAV: wr, Stream: info, TickRate: clock.MustRate(90000, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cut(Event{Name: "early", StartTick: 1000, EndTick: 2000}); !fault.IsKind(err, fault.KindMissingKeyframe) {
		t.Fatalf("err = %v, want missing-keyframe", err)
	}
}
