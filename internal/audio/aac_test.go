package audio

import (
	"testing"
)

func aacFrame(zeroTail bool) []byte {
	b := make([]byte, 32)
	b[0], b[1] = 0xFF, 0xF1
	if !zeroTail {
		for i := AAPayloadStart; i < len(b); i++ {
			b[i] = byte(i * 7)
		}
	}
	return b
}

const AAPayloadStart = AACHeaderBytes

func TestAACReportsOnlyZeroSpectralFramesAsSilent(t *testing.T) {
	frames := [][]byte{}
	for i := 0; i < 20; i++ {
		frames = append(frames, aacFrame(i >= 5 && i < 15))
	}
	res, err := AnalyzeAAC(9, frames, 0, 20_000_000, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Detectable || len(res.Silence) != 1 {
		t.Fatalf("detectable=%v silence=%v", res.Detectable, res.Silence)
	}
	if res.Silence[0].Start != 100_000_000 || res.Silence[0].End != 300_000_000 {
		t.Fatalf("silence=%+v", res.Silence[0])
	}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAACNeverInventsSilenceOnBadInput(t *testing.T) {
	cases := map[string][][]byte{
		"no frames":   nil,
		"empty frame": {{}},
	}
	for name, frames := range cases {
		res, err := AnalyzeAAC(9, frames, 0, 20_000_000, DefaultConfig())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Detectable || len(res.Silence) != 0 {
			t.Fatalf("%s: fabricated silence: %+v", name, res)
		}
	}
	res, _ := AnalyzeAAC(9, [][]byte{aacFrame(true)}, 0, 0, DefaultConfig())
	if res.Detectable {
		t.Fatal("unknown frame duration must be undetectable")
	}
}

func TestAACShortSilenceSuppressed(t *testing.T) {
	frames := [][]byte{aacFrame(true), aacFrame(true)}
	res, err := AnalyzeAAC(9, frames, 0, 20_000_000, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Silence) != 0 {
		t.Fatalf("40ms of silence below the 200ms floor was reported: %v", res.Silence)
	}
}

func TestFrameDuration(t *testing.T) {
	d, err := FrameDurationNS(1024, 48000)
	if err != nil || d != 21_333_333 {
		t.Fatalf("duration=%d err=%v", d, err)
	}
	if _, err := FrameDurationNS(0, 48000); err == nil {
		t.Fatal("expected error")
	}
}
