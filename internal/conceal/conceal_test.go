package conceal

import (
	"testing"

	"jbbench/internal/protocol"
)

func tone(v int16) []int16 {
	s := make([]int16, protocol.SamplesPerFrame)
	for i := range s {
		s[i] = v
	}
	return s
}

func TestHoldThenAttenuationThenSilence(t *testing.T) {
	c := New(DefaultConfig())
	c.Real(tone(1000))

	hold := c.Missing()
	if hold.Kind != KindHold || hold.Samples[0] != 500 {
		t.Fatalf("hold frame: kind=%s sample=%d", hold.Kind, hold.Samples[0])
	}
	att := c.Missing()
	if att.Kind != KindAtten || att.Samples[0] != 250 {
		t.Fatalf("attenuated frame: kind=%s sample=%d", att.Kind, att.Samples[0])
	}
	// Exhaust the allowed loss run; everything past the threshold is silence.
	for i := 0; i < c.cfg.SilenceThreshold; i++ {
		c.Missing()
	}
	sil := c.Missing()
	if sil.Kind != KindSilence || sil.Energy != 0 {
		t.Fatalf("silence frame: kind=%s energy=%v", sil.Kind, sil.Energy)
	}
	st := c.Stats()
	if st.ConcealedFrames == 0 || st.MaxConcealRun != int(st.ConcealedFrames) {
		t.Fatalf("stats wrong: %+v", st)
	}
	if st.SilenceFrames == 0 {
		t.Fatal("silence frames not counted")
	}
}

func TestRecoveryCrossfadeAndStats(t *testing.T) {
	c := New(DefaultConfig())
	c.Real(tone(1000))
	c.Missing()
	c.Missing()
	rec := c.Real(tone(2000))
	if rec.Kind != KindRecover {
		t.Fatalf("recovery kind = %s", rec.Kind)
	}
	// First crossfade sample blends old (attenuated) and new content.
	if rec.Samples[0] == 2000 {
		t.Fatal("first recovered sample was not crossfaded")
	}
	// Outside the crossfade region the frame is exactly the received content.
	if rec.Samples[len(rec.Samples)-1] != 2000 {
		t.Fatal("tail of recovered frame must equal received samples")
	}
	st := c.Stats()
	if st.RecoveryEpisodes != 1 || st.MaxConcealRun != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if st.MaxRecoveryJump <= 0 {
		t.Fatal("recovery jump not recorded")
	}

	// A subsequent in-order frame is plain real again.
	plain := c.Real(tone(1000))
	if plain.Kind != KindReal {
		t.Fatalf("kind after recovery = %s", plain.Kind)
	}
	if st2 := c.Stats(); st2.RecoveryEpisodes != 1 {
		t.Fatalf("episode count changed: %+v", st2)
	}
}

func TestConcealedFramesAreNotReceived(t *testing.T) {
	// The concealer only produces substitute frames; counters it exposes must
	// never be mistaken for network reception. This is enforced by the Stats
	// shape itself: no "received" field exists here.
	c := New(DefaultConfig())
	c.Real(tone(100))
	for i := 0; i < 4; i++ {
		f := c.Missing()
		if f.LossRun != i+1 {
			t.Fatalf("loss run = %d, want %d", f.LossRun, i+1)
		}
	}
	if got := c.RunLength(); got != 4 {
		t.Fatalf("RunLength = %d, want 4", got)
	}
}

func TestConfigValidation(t *testing.T) {
	c := New(Config{SilenceThreshold: -3, CrossfadeSamples: 1 << 20})
	if c.cfg.SilenceThreshold <= 0 {
		t.Fatal("silence threshold not defaulted")
	}
	if c.cfg.CrossfadeSamples > protocol.SamplesPerFrame {
		t.Fatal("crossfade length not clamped")
	}
}

func TestEnergyMonotonicUnderAttenuation(t *testing.T) {
	c := New(DefaultConfig())
	c.Real(tone(2000))
	prev := c.Missing().Energy
	for i := 0; i < 4; i++ {
		e := c.Missing().Energy
		if e > prev {
			t.Fatalf("energy grew during attenuation: %v -> %v", prev, e)
		}
		prev = e
	}
}
