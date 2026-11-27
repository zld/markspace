package transform

import (
	"encoding/binary"
	"testing"

	"continuity/internal/model"
)

func makePCM(rate uint32, layout model.ChannelLayout) []byte {
	perFrame := int(rate) / 50
	ch := layout.Channels()
	buf := make([]byte, perFrame*ch*2)
	for i := 0; i < perFrame*ch; i++ {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(i%1000)-500))
	}
	return buf
}

func TestFrameResamplesAndNormalizes(t *testing.T) {
	src := makePCM(48000, model.LayoutMono)
	res, err := Frame(src, Params{
		SourceRate: 48000, TargetRate: 24000,
		Source: model.LayoutMono, Target: model.LayoutStereo, TargetLUFS: -23,
	})
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}
	// 24 kHz 20 ms = 480 frames, stereo *2 bytes.
	if want := 480 * 2 * 2; len(res.PCM) != want {
		t.Fatalf("output length = %d, want %d", len(res.PCM), want)
	}
}

func TestFrameIsDeterministic(t *testing.T) {
	src := makePCM(48000, model.LayoutMono)
	p := Params{SourceRate: 48000, TargetRate: 44100, Source: model.LayoutMono, Target: model.LayoutMono, TargetLUFS: -20}
	a, err := Frame(src, p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Frame(src, p)
	if err != nil {
		t.Fatal(err)
	}
	if model.Checksum(a.PCM) != model.Checksum(b.PCM) {
		t.Fatal("identical input produced different output bytes")
	}
}

func TestLoudnessConvergesToTarget(t *testing.T) {
	src := makePCM(48000, model.LayoutMono)
	res, err := Frame(src, Params{
		SourceRate: 48000, TargetRate: 48000,
		Source: model.LayoutMono, Target: model.LayoutMono, TargetLUFS: -18,
	})
	if err != nil {
		t.Fatal(err)
	}
	drift := res.MeasuredLUFS - (-18.0)
	if drift < 0 {
		drift = -drift
	}
	if drift > 1.0 {
		t.Fatalf("measured LUFS %.3f drifts %.3f from -18", res.MeasuredLUFS, drift)
	}
}

func TestRejectsMalformedPayload(t *testing.T) {
	// Mono int16 payload must be a multiple of 2 bytes.
	if _, err := Frame([]byte{1, 2, 3}, Params{
		SourceRate: 48000, TargetRate: 48000,
		Source: model.LayoutMono, Target: model.LayoutMono, TargetLUFS: -23,
	}); err == nil {
		t.Fatal("malformed payload accepted")
	}
}

func TestMonoToStereoDoublesChannels(t *testing.T) {
	src := makePCM(48000, model.LayoutMono)
	mono, err := Frame(src, Params{
		SourceRate: 48000, TargetRate: 48000,
		Source: model.LayoutMono, Target: model.LayoutMono, TargetLUFS: -23,
	})
	if err != nil {
		t.Fatal(err)
	}
	stereo, err := Frame(src, Params{
		SourceRate: 48000, TargetRate: 48000,
		Source: model.LayoutMono, Target: model.LayoutStereo, TargetLUFS: -23,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stereo.PCM) != 2*len(mono.PCM) {
		t.Fatalf("stereo length %d != 2x mono %d", len(stereo.PCM), len(mono.PCM))
	}
}
