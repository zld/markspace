package continuity

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
)

func testInit() []byte {
	return BuildInit(InitSpec{Tracks: []TrackSpec{videoTrack(), audioTrack()}})
}

func videoFragment(base uint64, payloads ...[]byte) []byte {
	var samples []SampleSpec
	for i, p := range payloads {
		samples = append(samples, SampleSpec{Data: p, Duration: 3000, CompositionOffset: int32(i * 1500), Sync: i == 0})
	}
	return BuildFragment(FragmentSpec{BaseDecodeTime: base, TrackID: 1, Timescale: 90000, Samples: samples})
}

func TestParseInitTracks(t *testing.T) {
	init, err := ParseInit("io/init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	if len(init.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(init.Tracks))
	}
	v := init.Tracks[0]
	if v.Key != "video-avc1" || v.SourceID != 1 || v.Codec != CodecAVC || v.Width != 1280 {
		t.Fatalf("video=%+v", v)
	}
	if len(v.CodecConfig) != len(avcCConfig()) || !bytes.Equal(v.CodecConfig, avcCConfig()) {
		t.Fatalf("codec config not preserved")
	}
	a := init.Tracks[1]
	if a.Key != "audio-mp4a" || a.SampleRate != 48000 || a.ChannelCount != 2 {
		t.Fatalf("audio=%+v", a)
	}
	if !bytes.Equal(a.CodecConfig, []byte{0x12, 0x10}) {
		t.Fatalf("aac config=%x", a.CodecConfig)
	}
}

func TestParseInitFaults(t *testing.T) {
	t.Run("missing moov", func(t *testing.T) {
		data := BuildInit(InitSpec{OmitMoov: true, Tracks: []TrackSpec{videoTrack()}})
		_, err := ParseInit("init.mp4", data)
		if err == nil || !IsKind(err, KindParse) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("truncated moov", func(t *testing.T) {
		data := testInit()
		_, err := ParseInit("init.mp4", data[:len(data)-20])
		if err == nil {
			t.Fatal("expected failure")
		}
		if loc := LocationOf(err); loc.File != "init.mp4" {
			t.Fatalf("loc=%+v", loc)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if _, err := ParseInit("init.mp4", nil); err == nil || !IsKind(err, KindParse) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestParseSegmentSamples(t *testing.T) {
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	segBytes := videoFragment(90000, pattern(1, 40), pattern(2, 24), pattern(3, 31))
	seg, err := ParseSegment(init, "video/seg1.m4s", segBytes, "video-avc1")
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.Samples) != 3 || seg.TrackID != 1 || seg.BaseDecodeTime != 90000 {
		t.Fatalf("segment=%+v", seg)
	}
	s0 := seg.Samples[0]
	if s0.DTS != 90000 || s0.PTS != 90000 || s0.Duration != 3000 || !s0.Sync {
		t.Fatalf("sample0=%+v", s0)
	}
	s1 := seg.Samples[1]
	if s1.DTS != 93000 || s1.PTS != 94500 || s1.Sync {
		t.Fatalf("sample1=%+v", s1)
	}
	if !bytes.Equal(s1.Data, pattern(2, 24)) {
		t.Fatal("sample bytes were not preserved verbatim")
	}
	if s1.ByteOffset <= 0 || s1.ByteLength != 24 {
		t.Fatalf("byte range=%d+%d", s1.ByteOffset, s1.ByteLength)
	}
	if int(s1.ByteOffset+s1.ByteLength) > len(segBytes) {
		t.Fatal("byte range exceeds segment")
	}
}

func TestParseSegmentTruncatedBoxReportsOffset(t *testing.T) {
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	segBytes := BuildFragment(FragmentSpec{
		TrackID: 1, Timescale: 90000,
		Samples:       []SampleSpec{{Data: pattern(9, 64), Duration: 3000, Sync: true}},
		TruncateBytes: 16,
	})
	_, err = ParseSegment(init, "video/trunc.m4s", segBytes, "video-avc1")
	if err == nil {
		t.Fatal("truncated segment must fail")
	}
	if !IsKind(err, KindTruncated) {
		t.Fatalf("kind=%v", err)
	}
	loc := LocationOf(err)
	if loc.File != "video/trunc.m4s" || loc.Offset < 0 {
		t.Fatalf("loc=%+v", loc)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("video/trunc.m4s@")) {
		t.Fatalf("error lacks offset: %v", err)
	}
}

func TestParseSegmentRejectsUnknownTrack(t *testing.T) {
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	segBytes := BuildFragment(FragmentSpec{TrackID: 99, Timescale: 90000, Samples: []SampleSpec{{Data: pattern(1, 8), Duration: 1, Sync: true}}})
	_, err = ParseSegment(init, "x.m4s", segBytes, "video-avc1")
	if err == nil || !IsKind(err, KindMapping) {
		t.Fatalf("err=%v", err)
	}
}

func TestParseSegmentMissingMoof(t *testing.T) {
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSegment(init, "x.m4s", []byte{}, "video-avc1"); err == nil || !IsKind(err, KindParse) {
		t.Fatalf("err=%v", err)
	}
}

func TestParseSegmentPreservesAudioTiming(t *testing.T) {
	init, err := ParseInit("init.mp4", testInit())
	if err != nil {
		t.Fatal(err)
	}
	var samples []SampleSpec
	for i := 0; i < 4; i++ {
		samples = append(samples, SampleSpec{Data: pattern(byte(i), 20), Duration: 1024, Sync: true})
	}
	data := BuildFragment(FragmentSpec{BaseDecodeTime: 48000, TrackID: 2, Timescale: 48000, Samples: samples})
	seg, err := ParseSegment(init, "a.m4s", data, "audio-mp4a")
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.Samples) != 4 {
		t.Fatalf("samples=%d", len(seg.Samples))
	}
	if seg.Samples[0].DTS != 90000 || seg.Samples[1].DTS != 91920 {
		t.Fatalf("audio dts=%d,%d", seg.Samples[0].DTS, seg.Samples[1].DTS)
	}
	if seg.Samples[3].Duration != 1920 {
		t.Fatalf("audio duration=%d", seg.Samples[3].Duration)
	}
}

func TestParseRTPSequenceHeader(t *testing.T) {
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: 65534, Marker: true, PayloadType: 96, SSRC: 7},
		Payload: []byte{1, 2, 3},
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	seq, marker, err := ParseRTPSequenceHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 65534 || !marker {
		t.Fatalf("seq=%d marker=%v", seq, marker)
	}
	if _, _, err := ParseRTPSequenceHeader([]byte{0x80}); err == nil {
		t.Fatal("short header must fail")
	}
}
