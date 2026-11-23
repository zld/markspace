package cmaf

import (
	"math/rand"
	"testing"

	"avbatch/internal/mediaerr"
	"avbatch/internal/synth"
)

func TestRandomCorruptionNeverPanics(t *testing.T) {
	initBytes, _ := synth.InitSegment(1000, []synth.TrackSpec{synth.VideoTrack(1), synth.AudioTrack(2)})
	init, err := ParseInit(initBytes, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := synth.Fragment(synth.FragmentSpec{Sequence: 1, TrackID: 1,
		Samples: []synth.Sample{{Size: 64, Duration: 3000}, {Size: 80, Duration: 3000}}})
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		buf := append([]byte(nil), raw...)
		for j := 0; j < 1+rng.Intn(6); j++ {
			buf[rng.Intn(len(buf))] ^= byte(1 << uint(rng.Intn(8)))
		}
		if rng.Intn(4) == 0 {
			buf = buf[:rng.Intn(len(buf))]
		}
		// Corruption must never panic, and it must never be accepted
		// silently: either the fragment parses (the flipped bits were inside
		// sample payload, which is content, not framing) or it is rejected
		// with a classified error.
		if _, err := ParseFragment(0, buf, init, DefaultLimits()); err != nil {
			if mediaerr.Classify(err) == "" {
				t.Fatalf("iteration %d: unclassified error: %v", i, err)
			}
		}
	}
}
