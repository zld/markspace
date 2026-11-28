package rtp

import (
	"math/rand"
	"testing"

	"mediarc/model"
)

func TestWindowShuffledStream(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 25; trial++ {
		depth := 1 + rng.Intn(6)
		window := mustWindow(t, depth)
		total := 40 + rng.Intn(40)
		order := rng.Perm(total)
		var frames []Frame
		for _, idx := range order {
			seq := uint16(idx)
			rel, err := push(t, window, newPacket(seq, uint32(idx)*1800))
			if err != nil && !model.IsCode(err, model.CodeDuplicatePacket) && !model.IsCode(err, model.CodeLatePacket) {
				t.Fatalf("push %d: %v", idx, err)
			}
			frames = append(frames, rel.Frames...)
		}
		rel := window.Flush()
		frames = append(frames, rel.Frames...)
		// Every delivered frame must be unique and strictly increasing.
		seen := map[uint64]bool{}
		var last uint64
		for i, frame := range frames {
			if seen[frame.ExtendedSeq] {
				t.Fatalf("seq %d delivered twice", frame.ExtendedSeq)
			}
			seen[frame.ExtendedSeq] = true
			if i > 0 && frame.ExtendedSeq <= last {
				t.Fatalf("frame order violated: %d after %d", frame.ExtendedSeq, last)
			}
			last = frame.ExtendedSeq
		}
		if window.MaxResident() > depth {
			t.Fatalf("resident %d exceeds depth %d", window.MaxResident(), depth)
		}
	}
}
