package fixture

import (
	"crypto/sha256"
	"testing"

	"mediasync/internal/clock"
	msrtp "mediasync/internal/rtp"
)

func digest(s *Stream) [32]byte {
	h := sha256.New()
	for _, rs := range s.Renditions {
		for _, dg := range rs.Datagrams {
			h.Write(dg.Data)
		}
		for _, sr := range rs.Reports {
			h.Write(sr.Data)
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func TestGenerateDeterministic(t *testing.T) {
	cfg, err := DefaultConfig(41, 3_000_000_000, []int{1_200_000, 2_400_000})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Generate(cfg, CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(cfg, CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	if digest(a) != digest(b) {
		t.Fatal("identical seed produced different stream bytes")
	}
	if len(a.Renditions) != 2 || len(a.Ground) != 2 {
		t.Fatal("rendition fan-out wrong")
	}
}

func TestGenerateLossAndReorder(t *testing.T) {
	cfg, _ := DefaultConfig(41, 3_000_000_000, []int{1_200_000})
	clean, _ := Generate(cfg, CaseClean)
	loss, err := Generate(cfg, CaseLoss)
	if err != nil {
		t.Fatal(err)
	}
	if len(loss.Renditions[0].Datagrams) >= len(clean.Renditions[0].Datagrams) {
		t.Fatal("loss scenario did not remove datagrams")
	}
	// Reordering should disturb original ordering somewhere.
	disordered := false
	prev := -1
	for _, dg := range loss.Renditions[0].Datagrams {
		if dg.OrigOrder < prev {
			disordered = true
			break
		}
		prev = dg.OrigOrder
	}
	if !disordered {
		t.Fatal("expected reordered datagrams in loss scenario")
	}
}

func TestGenerateWrapNearBoundary(t *testing.T) {
	cfg, _ := DefaultConfig(41, 3_000_000_000, []int{1_200_000})
	s, err := Generate(cfg, CaseWrap)
	if err != nil {
		t.Fatal(err)
	}
	ground := s.Ground[0]
	threshold := int64(clock.Uint32Mod) - int64(clock.DefaultClockRate)*5
	if int64(ground[0].RawRTP)+0 < threshold {
		t.Fatal("wrap scenario did not start near the 32-bit boundary")
	}
	wrapped := false
	for i := 1; i < len(ground); i++ {
		if ground[i].RawRTP < ground[i-1].RawRTP {
			wrapped = true
		}
		if ground[i].ExtendedRTP <= ground[i-1].ExtendedRTP {
			t.Fatal("ground-truth extended RTP not monotonic across wrap")
		}
	}
	if !wrapped {
		t.Fatal("no 32-bit wrap occurred during generation")
	}
}

func TestGenerateCorruptMarksTruncation(t *testing.T) {
	cfg, _ := DefaultConfig(41, 3_000_000_000, []int{1_200_000})
	s, err := Generate(cfg, CaseCorrupt)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := 0
	for _, dg := range s.Renditions[0].Datagrams {
		if dg.Truncated || dg.Corrupt {
			corrupt++
		}
	}
	if corrupt == 0 {
		t.Fatal("corrupt scenario produced no damaged datagrams")
	}
}

func TestSenderReportsProduced(t *testing.T) {
	cfg, _ := DefaultConfig(41, 3_000_000_000, []int{1_200_000})
	s, _ := Generate(cfg, CaseClean)
	if len(s.Renditions[0].Reports) < 2 {
		t.Fatal("expected at least two sender reports over 3s")
	}
	// Reports must parse back through the production RTCP parser.
	for _, bundle := range s.Renditions[0].Reports {
		sr, err := msrtp.ParseSenderReport(bundle.Data, bundle.ArrivalNS)
		if err != nil {
			t.Fatalf("generated SR failed to parse: %v", err)
		}
		if sr.SSRC != s.Renditions[0].SSRC {
			t.Fatal("SR SSRC mismatch")
		}
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := DefaultConfig(1, 1_000_000_000, nil); err == nil {
		t.Fatal("empty bitrate list accepted")
	}
	cfg, _ := DefaultConfig(1, 1_000_000_000, []int{1000})
	cfg.LossRate = 2
	if _, err := Generate(cfg, CaseClean); err == nil {
		t.Fatal("loss rate above bound accepted")
	}
}

func TestParseDurationNS(t *testing.T) {
	ns, err := ParseDurationNS("30s")
	if err != nil || ns != 30_000_000_000 {
		t.Fatalf("30s -> %d, %v", ns, err)
	}
	if _, err := ParseDurationNS("30"); err == nil {
		t.Fatal("unitless duration accepted")
	}
	if _, err := ParseDurationNS("xs"); err == nil {
		t.Fatal("non-numeric duration accepted")
	}
}
