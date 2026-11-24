package fixtures

import (
	"testing"

	"github.com/pion/rtp"
)

func TestRTPStreamRoundTrip(t *testing.T) {
	data, err := RTPStream(RTPSpec{
		Timescale: 90000, RefTimescale: 1000, Count: 4, StartSeq: 65534,
		StartTS: 4294967290, StepTS: 3000, StepRef: 33,
	})
	if err != nil {
		t.Fatal(err)
	}
	h, samples, err := ParseRTPStream(data)
	if err != nil {
		t.Fatal(err)
	}
	if h.Count != 4 || h.Timescale != 90000 || h.RefTimescale != 1000 {
		t.Fatalf("header %+v", h)
	}
	if samples[0].Packet.SequenceNumber != 65534 || samples[1].Packet.SequenceNumber != 65535 {
		t.Fatalf("sequence wrap: %v", samples[1].Packet.SequenceNumber)
	}
	if samples[0].Packet.Timestamp != 4294967290 {
		t.Fatalf("start ts %d", samples[0].Packet.Timestamp)
	}
	// 4294967290 + 3000 wraps past 2^32.
	if samples[1].Packet.Timestamp != 4294967290+3000-1<<32 {
		t.Fatalf("wrapped ts %d", samples[1].Packet.Timestamp)
	}
	if samples[3].RefTick != 99 {
		t.Fatalf("ref ticks %d", samples[3].RefTick)
	}
}

func TestRTPStreamDriftExactness(t *testing.T) {
	base := RTPSpec{Timescale: 90000, RefTimescale: 90000, Count: 100, StepTS: 1000, StepRef: 1000}
	fast := base
	fast.DriftPPM = 1000
	a, err := RTPStream(base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RTPStream(fast)
	if err != nil {
		t.Fatal(err)
	}
	_, sa, _ := ParseRTPStream(a)
	_, sb, _ := ParseRTPStream(b)
	if sa[99].Packet.Timestamp == sb[99].Packet.Timestamp {
		t.Fatal("drifted stream must differ by the 100th packet")
	}
	if sa[0].Packet.Timestamp != sb[0].Packet.Timestamp {
		t.Fatal("drift must not change the first packet")
	}
}

func TestRTPStreamRejectsBadSpec(t *testing.T) {
	for _, spec := range []RTPSpec{
		{Count: 0, Timescale: 1, RefTimescale: 1, StepTS: 1, StepRef: 1},
		{Count: 1, Timescale: 0, RefTimescale: 1, StepTS: 1, StepRef: 1},
		{Count: 1, Timescale: 1, RefTimescale: 1, StepTS: 0, StepRef: 1},
	} {
		if _, err := RTPStream(spec); err == nil {
			t.Fatalf("spec %+v must fail", spec)
		}
	}
	if _, _, err := ParseRTPStream([]byte("nope")); err == nil {
		t.Fatal("bad magic must fail")
	}
	var _ = rtp.Packet{}
}
