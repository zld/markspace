package render

import (
	"math"
	"testing"

	"replayctl/internal/fixture"
)

func TestRenderAcceptanceWindowMetrics(t *testing.T) {
	paths, err := fixture.GenerateSelf(fixture.Options{Root: t.TempDir(), Case: fixture.CaseOK})
	if err != nil {
		t.Fatal(err)
	}
	r := New()
	defer func() {
		for _, s := range r.Sources {
			_ = s.Close()
		}
	}()

	art, err := r.Render(paths.Timeline, 500, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if art.Frames != 25 {
		t.Fatalf("frames=%d want25", art.Frames)
	}
	if art.Pixels != 25*16*16 {
		t.Fatalf("pixels=%d want6400", art.Pixels)
	}
	if art.AudioSamples != 48_000 {
		t.Fatalf("audio=%d want48000", art.AudioSamples)
	}
	if art.Peak != 8192 {
		t.Fatalf("peak=%d want8192", art.Peak)
	}
	db := 20 * math.Log10(float64(art.Peak)/32768.0)
	if math.Abs(db+12.04) > 0.01 {
		t.Fatalf("peak dbfs=%.2f", db)
	}
	if art.VideoAlignErrorMS > 20 {
		t.Fatalf("video alignment=%dms", art.VideoAlignErrorMS)
	}
}
