package ts

import (
	"testing"

	"example.com/assetpack/internal/fault"
)

func TestPCRWrapAccepted(t *testing.T) {
	// Start near the 33-bit modulus, cross it with a contiguous PID.
	start := PCRModulus - 90000
	var data []byte
	cc := uint8(0)
	vals := []uint64{start, start + 90000, 0, 90000, 180000}
	for _, v := range vals {
		data = append(data, buildPacket(pktSpec{pid: 0x300, cc: cc, pcr: pcrPtr(v)})...)
		cc++
	}
	s := Parse(data)
	if s.Stats.PCRWraps != 1 {
		t.Fatalf("expected exactly one accepted wrap, got %d (%+v)", s.Stats.PCRWraps, s.Faults.Items)
	}
	last := s.Packets[len(s.Packets)-1]
	wantBase := PCRModulus + 180000
	if last.PCRUnwrapped != wantBase {
		t.Fatalf("unwrapped %d want %d", last.PCRUnwrapped, wantBase)
	}
	if last.PCRWraps != 1 {
		t.Fatalf("wrap count not recorded on packet: %d", last.PCRWraps)
	}
	// Monotonic nanoseconds throughout; raw value recorded on every packet.
	for i := 1; i < len(s.Packets); i++ {
		if s.Packets[i].PCRNanos < s.Packets[i-1].PCRNanos {
			t.Fatalf("PCR time regressed at %d", i)
		}
		if !s.Packets[i].HasPCR || s.Packets[i].PCRRaw == 0 && i > 1 && vals[i] != 0 {
			t.Fatalf("raw PCR not retained at %d: %+v", i, s.Packets[i])
		}
	}
	if s.Packets[2].PCRRaw != 0 {
		t.Fatalf("raw wrap value should be zero: %d", s.Packets[2].PCRRaw)
	}
}

func TestPCRWrapRejectedOnDiscontinuity(t *testing.T) {
	start := PCRModulus - 90000
	var data []byte
	data = append(data, buildPacket(pktSpec{pid: 0x301, cc: 0, pcr: pcrPtr(start)})...)
	// Wrap-looking step but the discontinuity indicator is set.
	data = append(data, buildPacket(pktSpec{pid: 0x301, cc: 1, pcr: pcrPtr(1000), disc: true})...)
	s := Parse(data)
	if s.Stats.PCRWraps != 0 {
		t.Fatal("wrap accepted despite discontinuity indicator")
	}
	if findFault(s.Faults, fault.KindPCR) == nil {
		t.Fatalf("discontinuity not reported: %+v", s.Faults.Items)
	}
	last := s.Packets[len(s.Packets)-1]
	if last.PCRUnwrapped != start {
		t.Fatalf("rejected PCR must retain published time, got %d", last.PCRUnwrapped)
	}
}

func TestPCRWrapRejectedWithoutCCProof(t *testing.T) {
	start := PCRModulus - 90000
	var data []byte
	data = append(data, buildPacket(pktSpec{pid: 0x302, cc: 0, pcr: pcrPtr(start)})...)
	// Same PID, CC jumps 0 -> 7 while the PCR appears to wrap.
	data = append(data, buildPacket(pktSpec{pid: 0x302, cc: 7, pcr: pcrPtr(500)})...)
	s := Parse(data)
	if s.Stats.PCRWraps != 0 {
		t.Fatal("wrap accepted without continuity proof")
	}
	if s.Packets[len(s.Packets)-1].PCRUnwrapped != start {
		t.Fatal("unproven wrap mutated the timeline")
	}
}

func TestUnwrapPCRPure(t *testing.T) {
	prev := PCRModulus - 5
	got, wrapped := UnwrapPCR(prev, prev, 3, true, false)
	if !wrapped || got != PCRModulus+3 {
		t.Fatalf("unwrap failed: %d %v", got, wrapped)
	}
	if _, wrapped := UnwrapPCR(prev, prev, 3, false, false); wrapped {
		t.Fatal("unwrap accepted without CC proof")
	}
	if _, wrapped := UnwrapPCR(prev, prev, 3, true, true); wrapped {
		t.Fatal("unwrap accepted across discontinuity")
	}
	// Small backwards jog is not a wrap.
	if _, wrapped := UnwrapPCR(1000, 1000, 900, true, false); wrapped {
		t.Fatal("small regression treated as wrap")
	}
}
