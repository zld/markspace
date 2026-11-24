package codec

import (
	"testing"

	"example.com/media-kernel/media"
)

func TestParseOpusVariableFrameDurations(t *testing.T) {
	cases := []struct {
		name   string
		config int
		want   int64
	}{
		{"silk-10ms", 0, 480},
		{"silk-60ms", 3, 2880},
		{"celt-2.5ms", 16, 120},
		{"celt-20ms", 19, 960},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseOpus(BuildOpusPacket(tc.config, true, 24))
			if err != nil {
				t.Fatal(err)
			}
			if info.Samples != tc.want {
				t.Fatalf("samples=%d want %d", info.Samples, tc.want)
			}
			if info.Duration() <= 0 {
				t.Fatalf("duration not positive: %v", info.Duration())
			}
		})
	}
}

func TestParseOpusCode1DoublesDuration(t *testing.T) {
	pkt := []byte{(1 << 3) | 1, 1, 2, 3, 4} // config 1, code 1, 4 body bytes
	info, err := ParseOpus(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 2 || info.Samples != 1920 {
		t.Fatalf("frames=%d samples=%d want 2/1920", info.Frames, info.Samples)
	}
}

func TestParseOpusTruncatedCode2IsLocatable(t *testing.T) {
	pkt := []byte{0x02, 200} // code 2 claims a 200-byte first frame but ends
	_, err := ParseOpus(pkt)
	if err == nil || !media.HasCode(err, media.CodePacketTruncated) {
		t.Fatalf("err=%v want PACKET_TRUNCATED", err)
	}
	if e, ok := media.AsError(err); !ok || e.Offset != 1 {
		t.Fatalf("structured error=%#v missing offset 1", err)
	}
}

func TestParseAACADTSDuration(t *testing.T) {
	frame := BuildADTSFrame(3, 2, 0, 32) // 48 kHz stereo, one raw block
	info, err := ParseAAC(frame)
	if err != nil {
		t.Fatal(err)
	}
	if info.SampleRateHz != 48000 || info.Channels != 2 || info.Samples != 1024 {
		t.Fatalf("info=%+v", info)
	}

	three := BuildADTSFrame(4, 1, 2, 48) // 44.1 kHz, three raw blocks
	info, err = ParseAAC(three)
	if err != nil {
		t.Fatal(err)
	}
	if info.Samples != 3072 || info.SampleRateHz != 44100 {
		t.Fatalf("three-block info=%+v", info)
	}
}

func TestParseAACTruncationHasByteRange(t *testing.T) {
	frame := BuildADTSFrame(3, 2, 0, 64)
	_, err := ParseAAC(frame[:len(frame)-8])
	if err == nil || !media.HasCode(err, media.CodePacketTruncated) {
		t.Fatalf("err=%v want truncation", err)
	}
	e, ok := media.AsError(err)
	if !ok || e.ByteRange == nil || e.ByteRange.End != int64(len(frame)-8) {
		t.Fatalf("error=%#v missing terminal byte range", err)
	}
}

func TestAnalyzeAnnotatesErrorTimeRangeAndDoesNotZeroTimestamp(t *testing.T) {
	a := Analyze(TypeOpus, 42, 12345, nil)
	if a.Error == nil || a.Error.TimeRange == nil {
		t.Fatalf("expected structured time-ranged error, got %+v", a)
	}
	if a.Error.TimeRange.Start.Ticks != 12345 {
		t.Fatalf("error timestamp=%d want 12345", a.Error.TimeRange.Start.Ticks)
	}
	if a.Error.Seq == nil || *a.Error.Seq != 42 {
		t.Fatalf("error seq=%v want 42", a.Error.Seq)
	}
}
