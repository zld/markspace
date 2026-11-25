package sample

import "testing"

func TestGenerateDeterministicMultiTrackSample(t *testing.T) {
	a := Generate(17, 2)
	b := Generate(17, 2)
	if string(a.Metadata) != string(b.Metadata) {
		t.Fatal("metadata fixture is not deterministic")
	}
	if len(a.Reps) != 2 || len(a.Video) != 2 {
		t.Fatalf("expected two representations: %+v", a.Reps)
	}
	if len(a.Video[0]) != FrameCount || len(a.Audio) != FrameCount {
		t.Fatalf("unexpected frame counts video=%d audio=%d", len(a.Video[0]), len(a.Audio))
	}
	if len(a.Keyframes) != 6 || a.Keyframes[0] != 0 || a.Keyframes[len(a.Keyframes)-1] != 10_000_000_000 {
		t.Fatalf("unexpected keyframes: %v", a.Keyframes)
	}
	for i := range a.Video[0] {
		if string(a.Video[0][i].Payload) != string(b.Video[0][i].Payload) {
			t.Fatalf("payload %d is not seed-deterministic", i)
		}
	}
}

func TestBuildRTPStreamLossAndReorder(t *testing.T) {
	s := BuildRTPStream(12, 1, []int{0, 3})
	if len(s.Packets) != 12 || len(s.Wire) != 10 {
		t.Fatalf("unexpected packet counts sent=%d wire=%d", len(s.Packets), len(s.Wire))
	}
	if !s.Lost[0] || !s.Lost[3] {
		t.Fatal("loss set not recorded")
	}
}
