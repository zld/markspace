package rtp

import (
	"testing"
	"time"
)

func TestPacerOnlyAffectsRhythmNotData(t *testing.T) {
	// Two ingests process identical packets: one unpaced, one paced with
	// a fake sleep that records calls. Their blocks must be identical.
	build := func() *Ingest {
		return newTestIngest()
	}
	order := []uint16{0, 1, 3, 2, 4}

	run := func(pacer *Pacer) []Block {
		in := build()
		for _, seq := range order {
			pacer.Wait()
			in.PushPacket(marshalPkt(seq, uint32(seq)*testFrames, payloadFor(seq)))
		}
		blocks, _, _ := in.Flush()
		return blocks
	}

	paced := NewPacer(time.Millisecond)
	var fakeWaits int
	paced.sleep = func(time.Duration) { fakeWaits++ }

	unpacedBlocks := run(NewPacer(0))
	pacedBlocks := run(paced)

	if fakeWaits != len(order) {
		t.Fatalf("pacer applied %d waits want %d", fakeWaits, len(order))
	}
	if len(pacedBlocks) != len(unpacedBlocks) {
		t.Fatalf("pacing changed block count: %d vs %d", len(pacedBlocks), len(unpacedBlocks))
	}
	for i := range unpacedBlocks {
		a, b := unpacedBlocks[i], pacedBlocks[i]
		if a.StartSample != b.StartSample || a.EndSample != b.EndSample {
			t.Fatalf("pacing changed sample range at %d", i)
		}
		if string(a.PCM) != string(b.PCM) {
			t.Fatalf("pacing changed PCM bytes at block %d", i)
		}
	}
}

func TestPacerBandwidthChange(t *testing.T) {
	p := NewPacer(0)
	calls := 0
	p.sleep = func(time.Duration) { calls++ }
	p.Wait() // disabled: no call
	p.SetBandwidth(time.Nanosecond)
	p.Wait()
	p.Wait()
	if calls != 2 {
		t.Fatalf("bandwidth change did not enable pacing: calls=%d", calls)
	}
}
