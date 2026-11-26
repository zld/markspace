package event

import (
	"testing"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

func mkTrack(id uint32, durs []int64) *parse.Track {
	tr := &parse.Track{ID: id, Timescale: 1000, Kind: parse.KindVideo}
	t := int64(0)
	for i, d := range durs {
		tr.Samples = append(tr.Samples, parse.Sample{
			Number: i + 1, DTS: media.FromTicks(t, 1000),
			Duration: media.FromTicks(d, 1000), PTS: media.FromTicks(t, 1000),
			Offset: int64(i), Size: 10, Keyframe: i%2 == 0,
		})
		t += d
	}
	return tr
}

func mkAudio(id uint32, durs []int64) *parse.Track {
	tr := mkTrack(id, durs)
	tr.Kind = parse.KindAudio
	for i := range tr.Samples {
		tr.Samples[i].Keyframe = true
	}
	return tr
}

func packet(seq uint16, marker bool, ts uint32) Packet {
	return Packet{SequenceNumber: seq, Marker: marker, Timestamp: ts, SSRC: 7, PayloadType: 96}
}

func TestEventsMarkerOnly(t *testing.T) {
	s := Stream{ClockRate: 90000}
	evs, err := s.Events([]Packet{
		packet(1, false, 0),
		packet(2, true, 90000),
		packet(3, false, 180000),
		packet(4, true, 180000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events=%d", len(evs))
	}
	if evs[0].At.Cmp(media.FromMicros(1_000_000)) != 0 {
		t.Fatalf("event 0 at=%v", evs[0].At)
	}
	if evs[1].Index != 2 {
		t.Fatalf("index=%d", evs[1].Index)
	}
}

func TestEventsDuplicateSequenceCountedOnce(t *testing.T) {
	s := Stream{ClockRate: 90000}
	evs, err := s.Events([]Packet{
		packet(10, true, 9000),
		packet(10, true, 9000),
		packet(11, true, 18000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events=%d", len(evs))
	}
	if evs[0].Duplicates != 1 {
		t.Fatalf("duplicates=%d", evs[0].Duplicates)
	}
	if evs[1].Packet.SequenceNumber != 11 || evs[1].Index != 2 {
		t.Fatalf("event1=%#v", evs[1])
	}
}

func TestEventsDuplicateNonMarkerWinsOverMarker(t *testing.T) {
	// First delivery has no marker, the redelivery does. The sequence was
	// already seen, so no event is created.
	s := Stream{ClockRate: 90000}
	evs, _ := s.Events([]Packet{packet(5, false, 0), packet(5, true, 9000)})
	if len(evs) != 0 {
		t.Fatalf("events=%d", len(evs))
	}
}

func TestEventsBackwardsTimestampClamped(t *testing.T) {
	s := Stream{ClockRate: 90000}
	evs, err := s.Events([]Packet{
		packet(1, true, 90000),
		packet(2, true, 45000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if evs[1].At.Cmp(evs[0].At) != 0 {
		t.Fatalf("expected clamped time, got %v vs %v", evs[1].At, evs[0].At)
	}
}

func TestEventsRequiresClockRate(t *testing.T) {
	if _, err := (Stream{}).Events(nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestRTPSerialisationRoundTrip(t *testing.T) {
	in := packet(42, true, 123456)
	raw, err := MarshalPacket(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip %#v != %#v", out, in)
	}
	if _, err := ParsePacket([]byte{0xff, 0x00}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestBuildWindowsAlignsToKeyframeAndAudio(t *testing.T) {
	video := mkTrack(1, []int64{100, 100, 100, 100, 100, 100, 100, 100, 100, 100})
	audio := mkAudio(2, []int64{20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20})
	evs := []Event{{Index: 1, At: media.FromMillis(450)}}
	opts := BuildOptions{PreRoll: media.FromMillis(200), PostRoll: media.FromMillis(200)}
	wins, err := BuildWindows(evs, video, audio, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(wins) != 1 {
		t.Fatalf("windows=%d", len(wins))
	}
	w := wins[0]
	if w.Start.Cmp(media.FromMillis(250)) != 0 || w.End.Cmp(media.FromMillis(650)) != 0 {
		t.Fatalf("range %v..%v", w.Start, w.End)
	}
	if w.VideoStart.Cmp(media.FromMillis(200)) != 0 {
		t.Fatalf("video start %v", w.VideoStart)
	}
	if w.VideoDeviation.Cmp(media.FromMillis(50)) != 0 {
		t.Fatalf("video deviation %v", w.VideoDeviation)
	}
	// Audio aligns to the video-aligned start (200 ms), not to the requested
	// 250 ms boundary, so the clip's two tracks begin together.
	if w.AudioStart.Cmp(media.FromMillis(200)) != 0 {
		t.Fatalf("audio start %v", w.AudioStart)
	}
	if w.AudioDeviation.Cmp(media.Zero) != 0 {
		t.Fatalf("audio deviation %v", w.AudioDeviation)
	}
}

func TestBuildWindowsBeforeFirstKeyframeUsesFirst(t *testing.T) {
	video := mkTrack(1, []int64{100, 100, 100})
	video.Samples[1].Keyframe = false
	video.Samples[2].Keyframe = false
	evs := []Event{{Index: 1, At: media.FromMillis(50)}}
	wins, err := BuildWindows(evs, video, nil, BuildOptions{PreRoll: media.FromMillis(10), PostRoll: media.FromMillis(10)})
	if err != nil {
		t.Fatal(err)
	}
	if wins[0].VideoSample != 1 {
		t.Fatalf("video sample=%d", wins[0].VideoSample)
	}
}

func TestBuildWindowsNoVideo(t *testing.T) {
	if _, err := BuildWindows(nil, nil, nil, BuildOptions{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestPlanClipRanges(t *testing.T) {
	video := mkTrack(1, []int64{100, 100, 100, 100, 100})
	audio := mkAudio(2, []int64{20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20})
	evs := []Event{{Index: 1, At: media.FromMillis(250)}}
	wins, err := BuildWindows(evs, video, audio, BuildOptions{PreRoll: media.FromMillis(100), PostRoll: media.FromMillis(150)})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanClip(wins[0], video, audio)
	if err != nil {
		t.Fatal(err)
	}
	// The requested start is 150 ms; the latest keyframe at or before it is
	// the one at 0 ms. The exclusive end, 400 ms, includes the sample that
	// starts at 300 ms.
	if plan.Video.First != 0 || plan.Video.Last != 4 {
		t.Fatalf("video range %#v", plan.Video)
	}
	if plan.End.Cmp(media.FromMillis(400)) != 0 {
		t.Fatalf("end=%v", plan.End)
	}
	// Audio starts with the video-aligned boundary, which here is 0 ms
	// because the latest keyframe at or before 150 ms is the first sample.
	if plan.Audio.First != 0 || plan.Audio.Last != 20 {
		t.Fatalf("audio range %#v", plan.Audio)
	}
}
