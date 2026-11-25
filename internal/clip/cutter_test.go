package clip

import (
	"bytes"
	"encoding/binary"
	"testing"

	"replaycheck/internal/fault"
	"replaycheck/internal/stream"
	"replaycheck/internal/wav"
)

// fixture builds a 48 kHz stereo WAV and a 90 kHz video track whose
// keyframes land every keyEvery seconds.
type fixture struct {
	img    []byte
	wavImg []byte
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
		// A ramp so a peak is well defined and slice offsets are visible.
		v := int16((i * 7) % 20000)
		binary.LittleEndian.PutUint16(pcm[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(pcm[i*4+2:], uint16(v/2))
	}
	wavImg := wav.BuildPCM(2, rate, 16, pcm)
	wr, err := wav.NewReader(bytes.NewReader(wavImg), int64(len(wavImg)))
	if err != nil {
		t.Fatal(err)
	}
	// Video: 30 fps at 90 kHz, keyframe every keyEvery samples.
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
	c, err := New(&Source{WAV: wr, Stream: info})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{wavImg: wavImg, mp4Img: mp4Img, wr: wr, info: info, cutter: c}
}

func TestAudioBoundarySampleAligned(t *testing.T) {
	f := newFixture(t, 4, 30)
	// 48000 ticks per second == sample rate, so boundaries are exact except
	// when the event asks for a mid-sample instant.
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 48001, EndTick: 96000})
	if err != nil {
		t.Fatal(err)
	}
	if sl.Audio.StartFrame != 48001 {
		t.Fatalf("start frame = %d, want 48001", sl.Audio.StartFrame)
	}
	if sl.Audio.Frames != 96000-48001 {
		t.Fatalf("frames = %d, want %d", sl.Audio.Frames, 96000-48001)
	}
	if sl.Audio.Bytes != sl.Audio.Frames*4 {
		t.Fatalf("bytes = %d, want frames*4", sl.Audio.Bytes)
	}
}

func TestVideoStartsAtKeyframeNotAfter(t *testing.T) {
	f := newFixture(t, 4, 30) // keyframes at 0s and 1s
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 60000, EndTick: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if sl.Video.StartTick > sl.Event.StartTick {
		t.Fatalf("video starts at %d, after event start %d", sl.Video.StartTick, sl.Event.StartTick)
	}
	// Event starts at 60000 (0.667 s); last keyframe is 0.
	if sl.Video.StartTick != 0 {
		t.Fatalf("video start = %d, want 0", sl.Video.StartTick)
	}
	if sl.Video.LeadInTicks != 60000 {
		t.Fatalf("lead-in = %d, want 60000", sl.Video.LeadInTicks)
	}
	if sl.Video.LeadInMs != 666 {
		t.Fatalf("lead-in ms = %d, want 666", sl.Video.LeadInMs)
	}
	// Video must include all samples starting before 90000.
	for i := sl.Video.FirstSample; i <= sl.Video.LastSample; i++ {
		if f.info.Track.Samples[i].Tick >= 90000 {
			t.Fatalf("sample %d at %d is at/after event end", i, f.info.Track.Samples[i].Tick)
		}
	}
	if sl.Video.Samples != sl.Video.LastSample-sl.Video.FirstSample+1 {
		t.Fatalf("sample count mismatch")
	}
}

func TestEventOutOfRangeRejected(t *testing.T) {
	f := newFixture(t, 2, 30)
	if _, err := f.cutter.Cut(Event{Name: "x", StartTick: 0, EndTick: 48000 * 3}); !fault.IsKind(err, fault.KindOutOfRange) {
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
	// Event starts exactly on a sample and ends exactly on one: the end
	// sample must not be included.
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 48000, EndTick: 96000})
	if err != nil {
		t.Fatal(err)
	}
	if sl.Audio.StartFrame != 48000 || sl.Audio.EndFrame != 96000 {
		t.Fatalf("audio [%d,%d), want [48000,96000)", sl.Audio.StartFrame, sl.Audio.EndFrame)
	}
	for i := sl.Video.FirstSample; i <= sl.Video.LastSample; i++ {
		if f.info.Track.Samples[i].Tick == 96000 {
			t.Fatalf("video included sample exactly at end %d", 96000)
		}
	}
}

func TestCopyAudioLength(t *testing.T) {
	f := newFixture(t, 2, 30)
	sl, err := f.cutter.Cut(Event{Name: "e", StartTick: 0, EndTick: 48000})
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
	// Build a track whose first keyframe is not at zero by using a large
	// key interval and querying an event before the next keyframe but after
	// sample 0. Sample 0 is always a keyframe with KeyEvery, so instead we
	// drop sample 0 from the key list.
	const rate = 48000
	pcm := make([]byte, rate*2*2)
	wavImg := wav.BuildPCM(2, rate, 16, pcm)
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
	c, err := New(&Source{WAV: wr, Stream: info})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Cut(Event{Name: "early", StartTick: 1000, EndTick: 2000})
	if !fault.IsKind(err, fault.KindMissingKeyframe) {
		t.Fatalf("err = %v, want missing-keyframe", err)
	}
}
