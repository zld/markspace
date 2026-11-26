package validate

import (
	"testing"

	"replayctl/internal/fixture"
)

func TestMeasureGeneratedSource(t *testing.T) {
	p, err := fixture.GenerateSelf(fixture.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	m, err := MeasureFile(p.MediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.Frames != fixture.FrameCount {
		t.Fatalf("frames=%d want %d", m.Frames, fixture.FrameCount)
	}
	if m.Pixels != int64(fixture.FrameCount*fixture.Width*fixture.Height) {
		t.Fatalf("pixels=%d", m.Pixels)
	}
	if m.AudioSamples != fixture.AudioCount {
		t.Fatalf("audio=%d want %d", m.AudioSamples, fixture.AudioCount)
	}
	if len(m.SHA256) != 64 {
		t.Fatalf("hash length=%d", len(m.SHA256))
	}
}
