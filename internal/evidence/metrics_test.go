package evidence

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"math"
	"testing"

	"rtx/internal/model"
)

func rate(t *testing.T) model.Rate {
	t.Helper()
	r, err := model.NewRateRatio(720000, 1)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func grayJPEG(t *testing.T, y uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 64, 36))
	for i := range img.Pix {
		img.Pix[i] = y
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pcm(amplitude int16, samples int) []byte {
	b := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := amplitude
		if i%2 == 1 {
			v = -amplitude
		}
		binary.BigEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func sampleSet(t *testing.T) []Sample {
	r := rate(t)
	return []Sample{
		{TrackID: 1, Fragment: "f1", Start: model.MustTime(0, r), End: model.MustTime(28800, r), Data: grayJPEG(t, 96)},
		{TrackID: 1, Fragment: "f1", Start: model.MustTime(28800, r), End: model.MustTime(57600, r), Data: grayJPEG(t, 96)},
		{TrackID: 2, Fragment: "f1", Start: model.MustTime(0, r), End: model.MustTime(57600, r), Data: pcm(16384, 1440)},
	}
}

func TestComputeFixtureValues(t *testing.T) {
	m, err := Compute(sampleSet(t), TrackIDs{Video: 1, Audio: 2})
	if err != nil {
		t.Fatal(err)
	}
	if m.Frames != 2 {
		t.Fatalf("frames = %d", m.Frames)
	}
	if math.Abs(m.PixelMean-96) > 0.5 {
		t.Fatalf("pixel mean = %.3f, want 96", m.PixelMean)
	}
	// 16384/32768 = 0.5 -> -6.02 dBFS
	if math.Abs(m.PeakDBFS-(-6.0206)) > 0.01 {
		t.Fatalf("peak = %.4f, want -6.0206", m.PeakDBFS)
	}
	if m.AVSyncMS != 0 {
		t.Fatalf("av sync = %.3f", m.AVSyncMS)
	}
	if len(m.FrameSHA256) != 64 {
		t.Fatalf("frame hash length = %d", len(m.FrameSHA256))
	}
	if err := m.Validate(96, 2, -6, 1, 2); err != nil {
		t.Fatal(err)
	}
}

func TestComputeDeterministic(t *testing.T) {
	a, err := Compute(sampleSet(t), TrackIDs{Video: 1, Audio: 2})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compute(sampleSet(t), TrackIDs{Video: 1, Audio: 2})
	if err != nil {
		t.Fatal(err)
	}
	if a.FrameSHA256 != b.FrameSHA256 || a.PixelMean != b.PixelMean || a.PeakDBFS != b.PeakDBFS {
		t.Fatalf("metrics differ:\n%+v\n%+v", a, b)
	}
}

func TestComputeRejectsBadJPEG(t *testing.T) {
	samples := sampleSet(t)
	samples[0].Data = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}
	_, err := Compute(samples, TrackIDs{Video: 1, Audio: 2})
	if err == nil {
		t.Fatal("expected decode error")
	}
	if e, _ := model.As(err); e == nil || e.Code != CodeEvidence || !e.HasByte {
		t.Fatalf("error = %v", err)
	}
}

func TestComputeRejectsOddPCM(t *testing.T) {
	samples := sampleSet(t)
	samples[2].Data = []byte{1, 2, 3}
	if _, err := Compute(samples, TrackIDs{Video: 1, Audio: 2}); err == nil {
		t.Fatal("expected odd PCM error")
	}
}

func TestComputeRequiresBothTracks(t *testing.T) {
	samples := sampleSet(t)
	_, err := Compute(samples[:2], TrackIDs{Video: 1, Audio: 2})
	if err == nil {
		t.Fatal("expected missing audio error")
	}
}

func TestValidateTolerances(t *testing.T) {
	m := &Metrics{PixelMean: 96, PeakDBFS: -6, AVSyncMS: 1}
	if err := m.Validate(96, 2, -6, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(96, 2, -6, 1, 0.5); err == nil {
		t.Fatal("expected sync tolerance failure")
	}
	m.PixelMean = 100
	if err := m.Validate(96, 2, -6, 1, 2); err == nil {
		t.Fatal("expected pixel tolerance failure")
	}
}

func TestAVSyncDetectsOffset(t *testing.T) {
	r := rate(t)
	samples := []Sample{
		{TrackID: 1, Start: model.MustTime(0, r), End: model.MustTime(28800, r), Data: grayJPEG(t, 96)},
		// Audio starts 3 ms late.
		{TrackID: 2, Start: model.MustTime(2160, r), End: model.MustTime(30960, r), Data: pcm(1000, 10)},
	}
	m, err := Compute(samples, TrackIDs{Video: 1, Audio: 2})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.AVSyncMS-3) > 0.01 {
		t.Fatalf("av sync = %.3f, want 3", m.AVSyncMS)
	}
}
