package mp4mux

import "testing"

func TestAudioSamplesFoldsConcealment(t *testing.T) {
	audio := []AudioSample{
		{Data: nil, Duration: 480, Silence: true}, // leading concealment
		{Data: []byte("a"), Duration: 480},
		{Data: nil, Duration: 480, Silence: true}, // interior
		{Data: nil, Duration: 480, Silence: true}, // interior
		{Data: []byte("b"), Duration: 480},
		{Data: nil, Duration: 480, Silence: true}, // trailing
	}
	durs, sizes := audioSamples(audio)
	if len(durs) != len(sizes) {
		t.Fatalf("durs=%v sizes=%v", durs, sizes)
	}
	if len(sizes) != 2 {
		t.Fatalf("sizes = %v", sizes)
	}
	var total uint32
	for _, d := range durs {
		total += d
	}
	if total != 6*480 {
		t.Fatalf("total duration = %d want %d", total, 6*480)
	}
	if durs[0] != 960 {
		t.Fatalf("leading fold = %d", durs[0])
	}
	// The second real sample absorbs two interior concealment slots and the
	// trailing one.
	if durs[1] != 1920 {
		t.Fatalf("interior+trailing fold = %d", durs[1])
	}
}
