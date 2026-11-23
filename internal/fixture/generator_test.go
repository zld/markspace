package fixture

import (
	"testing"
)

func TestDeterministicGeneration(t *testing.T) {
	g1, g2 := NewGenerator(17), NewGenerator(17)
	a, _ := g1.GenerateAll()
	b, _ := g2.GenerateAll()
	if len(a) != len(b) || len(a) != 8 {
		t.Fatalf("want 8 fixtures, got %d/%d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Kind != b[i].Kind {
			t.Fatal("fixture order/identity must be deterministic")
		}
		if len(a[i].Events) != len(b[i].Events) {
			t.Fatal("event counts must be deterministic")
		}
		for j := range a[i].Events {
			if string(a[i].Events[j].Payload) != string(b[i].Events[j].Payload) {
				t.Fatal("payloads must be seed-deterministic")
			}
		}
	}
}

func TestFivePlusFaultClassesPresent(t *testing.T) {
	mats, _ := NewGenerator(17).GenerateAll()
	want := map[Kind]bool{
		KindReorderDup: false, KindSingleLoss: false, KindDoubleLoss: false,
		KindTruncatedMP4: false, KindBadTimescale: false, KindMissingRef: false,
	}
	for _, m := range mats {
		if _, ok := want[m.Kind]; ok {
			want[m.Kind] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("missing fault fixture: %s", k)
		}
	}
}

func TestSingleLossShape(t *testing.T) {
	mats, _ := NewGenerator(17).GenerateAll()
	var m *Material
	for i := range mats {
		if mats[i].Kind == KindSingleLoss {
			m = &mats[i]
		}
	}
	if m == nil {
		t.Fatal("single-loss fixture missing")
	}
	if len(m.Gaps) != 1 || m.Gaps[0].Count() != 1 {
		t.Fatalf("single-loss must declare exactly one missing packet: %+v", m.Gaps)
	}
	if len(m.FECGroups) != 1 || len(m.FECGroups[0].Members) < 2 {
		t.Fatal("single-loss must be covered by an FEC group")
	}
}

func TestDoubleLossShape(t *testing.T) {
	mats, _ := NewGenerator(17).GenerateAll()
	for _, m := range mats {
		if m.Kind == KindDoubleLoss {
			if m.Gaps[0].Count() != 2 {
				t.Fatalf("double-loss gap must span 2 packets: %+v", m.Gaps)
			}
		}
	}
}

func TestTruncatedAndBadTimescaleAndMissingRef(t *testing.T) {
	mats, _ := NewGenerator(17).GenerateAll()
	seen := map[Kind]bool{}
	for _, m := range mats {
		seen[m.Kind] = true
		switch m.Kind {
		case KindTruncatedMP4:
			if len(m.MP4Raw) == 0 {
				t.Fatal("truncated fixture must carry raw bytes")
			}
		case KindBadTimescale:
			if m.Timescale != 0 {
				t.Fatal("bad-timescale fixture must carry a zero timescale")
			}
		case KindMissingRef:
			if m.HLSPlaylist == "" || !contains(m.HLSPlaylist, "missing:") {
				t.Fatal("missing-reference fixture must carry an HLS playlist marker")
			}
		case KindColorClash:
			if len(m.ColorDecls) < 2 {
				t.Fatal("color-clash fixture must carry multiple declarations")
			}
		}
	}
	for _, k := range []Kind{KindTruncatedMP4, KindBadTimescale, KindMissingRef, KindColorClash} {
		if !seen[k] {
			t.Errorf("fixture %s not generated", k)
		}
	}
}

func TestLatencySamplesPositiveFinite(t *testing.T) {
	samples := NewGenerator(17).LatencySamples(1000)
	if len(samples) != 1000 {
		t.Fatalf("want 1000 samples, got %d", len(samples))
	}
	for _, s := range samples {
		if !(s > 0) {
			t.Fatalf("latency sample must be finite and positive, got %f", s)
		}
	}
	// Deterministic across generators with the same seed.
	again := NewGenerator(17).LatencySamples(1000)
	for i := range samples {
		if samples[i] != again[i] {
			t.Fatal("latency samples must be seed-deterministic")
		}
	}
}

func TestHealthyEventsHaveValidTimebase(t *testing.T) {
	mats, _ := NewGenerator(17).GenerateAll()
	for _, m := range mats {
		if m.Kind == KindHealthy {
			for _, ev := range m.Events {
				if !ev.Timebase.Valid() {
					t.Fatal("healthy events need a valid timebase")
				}
				if ev.Sequence == 0 && ev.Start.Ticks() != 0 {
					t.Fatal("first healthy event should start at zero")
				}
			}
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
