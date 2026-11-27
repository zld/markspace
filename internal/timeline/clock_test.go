package timeline

import "testing"

func TestConvertRoundTripWithinOneTick(t *testing.T) {
	audio := Clock{Timescale: 48000}
	video := Clock{Timescale: 90000}
	for _, ticks := range []Ticks{0, 1, 1024, 960, 48000, 123456789} {
		v, err := audio.Convert(ticks, video)
		if err != nil {
			t.Fatal(err)
		}
		back, err := video.Convert(v, audio)
		if err != nil {
			t.Fatal(err)
		}
		if d := back - ticks; d < -1 || d > 1 {
			t.Fatalf("round trip of %d drifted to %d", ticks, back)
		}
	}
}

func TestConvertExact(t *testing.T) {
	a := Clock{Timescale: 48000}
	b := Clock{Timescale: 90000}
	v, err := a.Convert(48000, b)
	if err != nil {
		t.Fatal(err)
	}
	if v != 90000 {
		t.Fatalf("1s on 48k clock = %d on 90k clock, want 90000", v)
	}
}

func TestFromRational(t *testing.T) {
	c := Clock{Timescale: 48000}
	v, err := c.FromRational(1, 50) // 20ms Opus frame
	if err != nil {
		t.Fatal(err)
	}
	if v != 960 {
		t.Fatalf("20ms at 48k = %d, want 960", v)
	}
}

func TestInvalidClock(t *testing.T) {
	if _, err := (Clock{Timescale: 0}).Convert(1, Clock{Timescale: 1}); err == nil {
		t.Fatal("expected error for zero timescale")
	}
}

func TestFaultMessage(t *testing.T) {
	f := &Fault{Track: "audio/rtp", Stage: StageIngest, Offset: 42, Seq: 7, Msg: "truncated packet"}
	want := "track=audio/rtp stage=ingest offset=42 seq=7: truncated packet"
	if f.Error() != want {
		t.Fatalf("got %q want %q", f.Error(), want)
	}
}
