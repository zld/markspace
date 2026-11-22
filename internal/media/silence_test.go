package media

import (
	"encoding/binary"
	"testing"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/timeline"
)

func pcm(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// silentFrame builds one 20 ms frame of near-zero PCM.
func silentFrame(startUS int64) AudioFrame {
	return AudioFrame{StartUS: startUS, EndUS: startUS + 20_000, PCM: pcm(10, -10, 5, -5)}
}

// loudFrame builds one 20 ms frame of high-amplitude PCM.
func loudFrame(startUS int64) AudioFrame {
	return AudioFrame{StartUS: startUS, EndUS: startUS + 20_000, PCM: pcm(20000, -20000, 18000, -18000)}
}

func TestDetectSilenceClassifiesLongQuietRun(t *testing.T) {
	cfg := DefaultSilenceConfig()
	var frames []AudioFrame
	for i := 0; i < 20; i++ { // 400 ms of silence, exceeds 200 ms minimum
		frames = append(frames, silentFrame(int64(i)*20_000))
	}
	out, err := DetectSilence(frames, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Status != manifest.StatusSilent {
		t.Fatalf("want one silent interval, got %+v", out)
	}
	if out[0].StartUS != 0 || out[0].EndUS != 400_000 {
		t.Fatalf("silence range = [%d,%d]", out[0].StartUS, out[0].EndUS)
	}
}

func TestDetectSilenceIgnoresShortRun(t *testing.T) {
	cfg := DefaultSilenceConfig()
	frames := []AudioFrame{silentFrame(0), silentFrame(20_000), loudFrame(40_000)} // 40 ms < 200 ms
	out, err := DetectSilence(frames, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range out {
		if s.Status == manifest.StatusSilent {
			t.Fatalf("sub-minimum silence reported: %+v", out)
		}
	}
}

func TestDetectSilencePartialBoundaryWindow(t *testing.T) {
	cfg := DefaultSilenceConfig()
	// Final frame is only 8 ms, shorter than the 20 ms analysis window.
	frames := []AudioFrame{
		silentFrame(0), silentFrame(20_000),
		{StartUS: 40_000, EndUS: 48_000, PCM: pcm(0, 0)},
	}
	out, err := DetectSilence(frames, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range out {
		if s.Status == manifest.StatusPartialWindow {
			found = true
			if s.StartUS != 40_000 || s.EndUS != 48_000 {
				t.Fatalf("partial window range wrong: %+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("partial-window status missing: %+v", out)
	}
}

func TestDetectSilenceTimestampRegression(t *testing.T) {
	cfg := DefaultSilenceConfig()
	frames := []AudioFrame{
		silentFrame(0), silentFrame(20_000),
		silentFrame(10_000), // moves backwards relative to runEnd 40000
	}
	out, err := DetectSilence(frames, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range out {
		if s.Status == manifest.StatusTimestampRegress {
			found = true
		}
		if s.Status == manifest.StatusSilent && s.StartUS == 10_000 {
			t.Fatal("regressed frame must not be classified silent")
		}
	}
	if !found {
		t.Fatalf("timestamp-regress status missing: %+v", out)
	}
}

// TestBurstLossAcrossSilence is the required fault class: a burst gap covering
// the analysis window must produce a gap-covered status, never silence, even
// though the surrounding audio is quiet.
func TestBurstLossAcrossSilence(t *testing.T) {
	cfg := DefaultSilenceConfig()
	frames := []AudioFrame{
		silentFrame(0), silentFrame(20_000),
		silentFrame(40_000), silentFrame(60_000), silentFrame(80_000),
	}
	// Gap covers the middle three frames (40 ms..100 ms).
	gaps := []manifest.LossGap{{StartUS: 40_000, EndUS: 100_000, MissingCount: 14}}
	out, err := DetectSilence(frames, gaps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	covered := 0
	for _, s := range out {
		if s.Status == manifest.StatusGapCovered {
			covered++
		}
	}
	if covered == 0 {
		t.Fatalf("gap-covered frames not reported: %+v", out)
	}
	// No silent interval may span the gap: the unknown region is excluded.
	for _, s := range out {
		if s.Status == manifest.StatusSilent && s.StartUS < 100_000 && s.EndUS > 40_000 {
			t.Fatalf("silence interval crosses the loss gap: %+v", s)
		}
	}
}

func TestDetectSilenceRejectsBadConfig(t *testing.T) {
	if _, err := DetectSilence(nil, nil, SilenceConfig{Window: 0}); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("zero window: %v", err)
	}
}

func TestRMS16(t *testing.T) {
	if r := rms16(pcm(0, 0)); r != 0 {
		t.Fatalf("rms of zeros = %v, want 0", r)
	}
	r := rms16(pcm(3, 4)) // sqrt((9+16)/2) = sqrt(12.5)
	if r < 3.53 || r > 3.54 {
		t.Fatalf("rms = %v, want ~3.535", r)
	}
	// Odd trailing byte must not panic or count as a sample.
	_ = rms16(append(pcm(100), 0xAB))
}
