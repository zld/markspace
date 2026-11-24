package track

import (
	"testing"

	"mediapack/internal/fault"
)

func TestRegisterAssignsStableOutputIndices(t *testing.T) {
	r := NewRegistry()
	// Register in shuffled input order; output indices follow registration
	// order, which the demuxer derives from track entry order.
	first, err := r.Register(3, "V_VP9", 1000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Register(7, "A_OPUS", 48000)
	if err != nil {
		t.Fatal(err)
	}
	third, err := r.Register(9, "S_TEXT/WEBVTT", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if first.OutputIndex != 1 || second.OutputIndex != 2 || third.OutputIndex != 3 {
		t.Fatalf("output indices not stable: %d %d %d",
			first.OutputIndex, second.OutputIndex, third.OutputIndex)
	}

	// Re-registering the same input number must not allocate a new index.
	again, err := r.Register(3, "V_VP9", 1000)
	if err != nil || again != first {
		t.Fatalf("re-register should be idempotent: %v", err)
	}
	if len(r.Mapping().Tracks) != 3 {
		t.Fatal("duplicate registration changed mapping size")
	}
}

func TestAllSupportedCodecs(t *testing.T) {
	r := NewRegistry()
	supported := []struct {
		num  uint32
		id   string
		kind Kind
	}{
		{1, "V_VP8", KindVideo},
		{2, "V_VP9", KindVideo},
		{3, "A_OPUS", KindAudio},
		{4, "A_VORBIS", KindAudio},
		{5, "S_TEXT/WEBVTT", KindSubtitle},
	}
	for _, c := range supported {
		tr, err := r.Register(c.num, c.id, 1000)
		if err != nil {
			t.Fatalf("codec %s rejected: %v", c.id, err)
		}
		if tr.Codec.Kind != c.kind {
			t.Errorf("codec %s kind=%s want %s", c.id, tr.Codec.Kind, c.kind)
		}
	}
}

func TestUnsupportedCodecFault(t *testing.T) {
	r := NewRegistry()
	_, err := r.Register(4, "V_MPEG4/ISO/AVC", 1000)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindUnsupportedCodec || fe.Track != 4 {
		t.Fatalf("expected unsupported-codec fault on track 4, got %v", err)
	}
}

func TestZeroTickRateRejected(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register(1, "V_VP9", 0); !fault.Is(err, fault.KindTimestampRegression) {
		t.Fatalf("expected tick-rate fault, got %v", err)
	}
}

func TestMappingDeterminismAndEquality(t *testing.T) {
	build := func() *Mapping {
		r := NewRegistry()
		_, _ = r.Register(2, "A_VORBIS", 1000)
		_, _ = r.Register(1, "V_VP8", 1000)
		return r.Mapping()
	}
	a, b := build(), build()
	if !a.Equal(b) {
		t.Fatal("identical inputs must produce equal mappings")
	}
	linesA, linesB := a.Render(), b.Render()
	for i := range linesA {
		if linesA[i] != linesB[i] {
			t.Fatalf("rendered mapping not deterministic: %q vs %q", linesA[i], linesB[i])
		}
	}
	if got := a.ByKind(KindVideo); len(got) != 1 || got[0].InputNumber != 1 {
		t.Fatalf("video selection wrong: %+v", got)
	}
	diff := NewRegistry().Mapping()
	if a.Equal(diff) {
		t.Fatal("mappings of different size must not be equal")
	}
}
