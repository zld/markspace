package testfixture

import "testing"

func TestApplyDamageProducesExpectedDispositions(t *testing.T) {
	spec := RTPSpec{Seed: 73, ClockRate: 48000, PacketCount: 100, BaseSeq: 0, PayloadSize: 40}
	packets, err := GenerateRTP(spec)
	if err != nil {
		t.Fatal(err)
	}
	dmg := DamageSpec{
		Seed: 73, LossBurst: 14, LossAt: 40, ReorderWindow: 32,
		DuplicateAt: 10, LateAt: 5,
	}
	out := ApplyDamage(packets, dmg)
	// 100 - 14 lost + 1 duplicate + 1 moved-late = 88 packets presented.
	want := 100 - 14 + 1 + 1
	if len(out) != want {
		t.Fatalf("damaged count = %d, want %d", len(out), want)
	}
	// Determinism: same spec yields identical byte streams.
	out2 := ApplyDamage(packets, dmg)
	if len(out2) != len(out) {
		t.Fatal("damage not deterministic")
	}
	for i := range out {
		if string(out[i]) != string(out2[i]) {
			t.Fatalf("packet %d differs across runs", i)
		}
	}
}

func TestApplyDamageNoLossKeepsAll(t *testing.T) {
	spec := RTPSpec{Seed: 1, ClockRate: 48000, PacketCount: 10, PayloadSize: 20}
	packets, _ := GenerateRTP(spec)
	out := ApplyDamage(packets, DamageSpec{Seed: 1, DuplicateAt: -1, LateAt: -1, ReorderWindow: 0})
	if len(out) != 10 {
		t.Fatalf("clean-ish stream count = %d, want 10", len(out))
	}
}
