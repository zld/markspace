package fixture

import (
	"testing"

	"mediascan/internal/fault"
	"mediascan/internal/manifest"
)

func TestScenarioNormalParsesInBothDialects(t *testing.T) {
	sc, err := BuildScenario(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	hm, hlog := manifest.ParseHLS(sc.HLS)
	if len(hm.Segments) != 4 || hlog.HasError() {
		t.Fatalf("HLS normal must be clean: %+v", hlog.Records())
	}
	dm, dlog := manifest.ParseDASH(sc.DASH)
	if len(dm.Segments) != 4 || dlog.HasError() {
		t.Fatalf("DASH normal must be clean: %+v", dlog.Records())
	}
	if dm.Segments[0].MediaSeq != 0 || dm.TimeScale != 1000 {
		t.Fatalf("DASH mapping wrong: %+v", dm.Segments[0])
	}
	if len(sc.RTPPackets) != 5 {
		t.Fatalf("RTP packet count = %d, want 5", len(sc.RTPPackets))
	}
}

func TestScenarioBoundaryFaults(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MissingSegment = true
	cfg.DuplicateSequence = true
	sc, err := BuildScenario(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hm, hlog := manifest.ParseHLS(sc.HLS)
	dm, dlog := manifest.ParseDASH(sc.DASH)
	if len(hm.Segments) != 4 || len(dm.Segments) != 4 {
		t.Fatal("boundary scenario still lists all four entries")
	}
	for name, log := range map[string]*fault.Log{"hls": hlog, "dash": dlog} {
		kinds := map[fault.Kind]bool{}
		for _, r := range log.Records() {
			kinds[r.Kind] = true
		}
		if !kinds[fault.KindMissingSegment] || !kinds[fault.KindDuplicateSequence] {
			t.Fatalf("%s missing expected faults: %+v", name, kinds)
		}
	}
	if sc.Segments[2].Present {
		t.Fatal("third segment must be marked absent")
	}
}

func TestScenarioDamagedAndWrap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TruncateBoxBytes = 17
	cfg.TimestampWrap = true
	cfg.VFR = true
	sc, err := BuildScenario(cfg)
	if err != nil {
		t.Fatal(err)
	}
	base, err := BuildScenario(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	target := sc.Segments[1]
	if len(target.Data) >= len(base.Segments[1].Data) {
		t.Fatal("truncation did not shrink the target segment")
	}
	if hasNoWrap(sc.RTPPackets) {
		t.Fatal("expected a 32-bit timestamp wrap in RTP fixtures")
	}
}

func hasNoWrap(packets [][]byte) bool {
	prev := uint32(0)
	for i, raw := range packets {
		// RTP timestamp starts at byte 4, four bytes big-endian.
		ts := uint32(raw[4])<<24 | uint32(raw[5])<<16 |
			uint32(raw[6])<<8 | uint32(raw[7])
		if i > 0 && ts < prev {
			return false
		}
		prev = ts
	}
	return true
}
