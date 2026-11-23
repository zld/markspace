package fixture

import (
	"bytes"
	"testing"

	"github.com/broadcast/mediad/internal/probe"
	"github.com/broadcast/mediad/internal/model"
)

func TestStandardSetCoversAllMandatedVariants(t *testing.T) {
	set := StandardSet(23)
	want := map[Corruption]bool{
		CorruptNone: false, CorruptTruncatedMdat: false,
		CorruptBackwardTimestamps: false, CorruptSparseKeyframes: false,
		CorruptNonZeroStart: false, CorruptChannelConflict: false,
		CorruptSampleRateConflict: false, CorruptColorConflict: false,
	}
	for _, s := range set {
		if _, ok := want[s.Corruption]; !ok {
			t.Fatalf("unexpected corruption %q", s.Corruption)
		}
		want[s.Corruption] = true
		if len(s.Tracks) < 2 {
			t.Fatalf("fixture %s must have >= 2 tracks", s.Name)
		}
	}
	for k, found := range want {
		if !found {
			t.Fatalf("missing mandated fixture variant %s", k)
		}
	}
}

func TestEveryFixtureIsProbeable(t *testing.T) {
	for _, spec := range StandardSet(23) {
		raw, err := Generate(spec)
		if err != nil {
			t.Fatalf("%s generate: %v", spec.Name, err)
		}
		errs := &model.ErrorSet{}
		g, _, err := probe.Probe(bytes.NewReader(raw), int64(len(raw)), probe.DefaultConfig(), errs)
		if err != nil {
			t.Fatalf("%s probe: %v", spec.Name, err)
		}
		if len(g.IDs()) < 2 {
			t.Fatalf("%s: want >= 2 tracks, got %d", spec.Name, len(g.IDs()))
		}
	}
}

func TestGenerationIsDeterministic(t *testing.T) {
	for _, spec := range StandardSet(23) {
		a, _ := Generate(spec)
		b, _ := Generate(spec)
		if !bytes.Equal(a, b) {
			t.Fatalf("fixture %s not byte-deterministic", spec.Name)
		}
	}
}

func TestSeedChangesPayloadBytes(t *testing.T) {
	a, _ := Generate(StandardSet(1)[0])
	b, _ := Generate(StandardSet(2)[0])
	if bytes.Equal(a, b) {
		t.Fatal("different seeds must produce different bytes")
	}
}
