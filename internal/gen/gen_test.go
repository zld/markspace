package gen_test

import (
	"testing"

	"mediacore/internal/gen"
)

func TestAudioFixtureDeterministicAndWraps(t *testing.T) {
	cfg := gen.DefaultAudio(42, 180, 4294967000, 250)
	a := gen.BuildAudio(cfg)
	b := gen.BuildAudio(cfg)
	if len(a.Packets) != len(b.Packets) || len(a.PCM) != cfg.Blocks {
		t.Fatalf("fixture shape wrong: packets=%d pcm=%d blocks=%d",
			len(a.Packets), len(a.PCM), cfg.Blocks)
	}
	// Determinism: identical seeds produce byte-identical payloads/order.
	for i := range a.Packets {
		if a.Packets[i].RecvNS != b.Packets[i].RecvNS ||
			string(a.Packets[i].Raw) != string(b.Packets[i].Raw) {
			t.Fatalf("packet %d differs across runs", i)
		}
	}
	// A 32-bit timestamp wrap must occur given the near-boundary start.
	sawWrap := false
	for i := 1; i < len(a.Packets); i++ {
		if a.Packets[i].RTPTS < a.Packets[i-1].RTPTS {
			sawWrap = true
		}
	}
	if !sawWrap {
		t.Fatal("expected an RTP timestamp wrap in the fixture")
	}
	// Dropped blocks are absent from arrivals but present in ground-truth PCM.
	arrived := map[int]bool{}
	for _, p := range a.Packets {
		arrived[p.BlockIdx] = true
	}
	for _, drop := range cfg.DropSeqs {
		if arrived[drop] {
			t.Fatalf("dropped block %d still arrived", drop)
		}
	}
	// The late block carries its injected delay.
	var lateCount int
	for _, p := range a.Packets {
		if p.Late {
			lateCount++
		}
	}
	if lateCount != 1 {
		t.Fatalf("expected exactly one late packet, got %d", lateCount)
	}
}

func TestVideoVFRCadenceAndKeyframes(t *testing.T) {
	cfr := gen.BuildVideo(gen.DefaultVideo(42, false))
	vfr := gen.BuildVideo(gen.DefaultVideo(42, true))
	if len(cfr.Deltas) != len(vfr.Deltas) {
		t.Fatal("sample count differs between CFR and VFR")
	}
	// VFR long frames are double the nominal CFR delta.
	if vfr.Deltas[1] != 2*cfr.Deltas[1] {
		t.Fatalf("VFR long-frame delta wrong: %d vs %d", vfr.Deltas[1], cfr.Deltas[1])
	}
	// Deterministic payloads across rebuilds.
	vfr2 := gen.BuildVideo(gen.DefaultVideo(42, true))
	if string(vfr.Data) != string(vfr2.Data) {
		t.Fatal("video fixtures are not deterministic")
	}
	keyCount := 0
	for _, k := range vfr.Keyframes {
		if k {
			keyCount++
		}
	}
	if keyCount == 0 {
		t.Fatal("no keyframes generated")
	}
}
