package rtp

import (
	"math"
	"testing"

	"github.com/pion/rtp"

	"example.com/mediavault/internal/clock"
	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fixtures"
)

func decode(t *testing.T, raw [][]byte) []rtp.Packet {
	t.Helper()
	out := make([]rtp.Packet, len(raw))
	for i, b := range raw {
		if err := out[i].Unmarshal(b); err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
	}
	return out
}

func TestReconcileVariablePacketLengths(t *testing.T) {
	rate := clock.NewRate(8000, 1)
	pattern := fixtures.VariablePattern()
	raw := fixtures.RTPPackets(fixtures.RTPOptions{SampleCounts: pattern, Start: 0xFFFFF000, DriftPPM: 250, FrameBytes: 2})
	tl, err := Reconcile(decode(t, raw), rate, 8000, 2, DefaultLimits())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(tl.Packets) != 32 {
		t.Fatalf("packets = %d", len(tl.Packets))
	}
	if tl.Wraps != 1 {
		t.Fatalf("wraps = %d, want 1", tl.Wraps)
	}
	distinct := make(map[int64]bool)
	for i, p := range tl.Packets {
		if p.Samples != pattern[i] {
			t.Fatalf("packet %d samples = %d, want its own payload count %d", i, p.Samples, pattern[i])
		}
		distinct[p.Samples] = true
	}
	if len(distinct) != 6 {
		t.Fatalf("packet lengths are not variable: %d distinct", len(distinct))
	}
	var total int64
	for _, p := range tl.Packets {
		total += p.Samples
	}
	if tl.TotalSamples != total || total != 175000 {
		t.Fatalf("total samples = %d, packets sum = %d", tl.TotalSamples, total)
	}
	if tl.DriftPPM < 240 || tl.DriftPPM > 260 {
		t.Fatalf("drift = %d ppm, want about 250", tl.DriftPPM)
	}
	if tl.Corrections == 0 {
		t.Fatal("drift corrections should be recorded")
	}
}

func TestReconcileRejectsDriftBeyondLimit(t *testing.T) {
	rate := clock.NewRate(8000, 1)
	raw := fixtures.RTPPackets(fixtures.RTPOptions{
		SampleCounts: fixtures.VariablePattern(), Start: 1000, DriftPPM: 1500, FrameBytes: 2,
	})
	_, err := Reconcile(decode(t, raw), rate, 8000, 2, DefaultLimits())
	if fault.CodeOf(err) != fault.CodeRTPDrift {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
	if fault.StageOf(err) != fault.StageNormalized {
		t.Fatalf("stage = %q", fault.StageOf(err))
	}
}

func TestReconcileRejectsBackwardWithoutWrap(t *testing.T) {
	rate := clock.NewRate(8000, 1)
	raw := fixtures.RTPPackets(fixtures.RTPOptions{SampleCounts: []int64{1000, 1000, 1000}, Start: 50_000, FrameBytes: 2})
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw[2]); err != nil {
		t.Fatal(err)
	}
	pkt.Timestamp = 40_000 // below the previous packet, too small to be a wrap
	raw[2], _ = pkt.Marshal()
	_, err := Reconcile(decode(t, raw), rate, 8000, 2, DefaultLimits())
	if fault.CodeOf(err) != fault.CodeRTPBackward {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestReconcileRejectsUnalignedPayload(t *testing.T) {
	rate := clock.NewRate(8000, 1)
	raw := [][]byte{}
	var p rtp.Packet
	p.Payload = make([]byte, 7) // odd byte count cannot be 16-bit frames
	raw = append(raw, mustMarshal(&p))
	_, err := Reconcile(decode(t, raw), rate, 8000, 2, DefaultLimits())
	if fault.CodeOf(err) != fault.CodeFrameAlign {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestConvertRejectsOverflow(t *testing.T) {
	huge := clock.NewRate(math.MaxInt64, 1)
	_, err := Convert(math.MaxInt64, 1, huge)
	if fault.CodeOf(err) != fault.CodeRTPOverflow {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
	if fault.StageOf(err) != fault.StageNormalized {
		t.Fatalf("stage = %q", fault.StageOf(err))
	}
}

func TestReconcileIsolatesOverflowingConversion(t *testing.T) {
	// Two 32-bit wraps push the extended timestamp high enough that even a
	// legitimate rate overflows when multiplied out.
	rate := clock.NewRate(math.MaxInt64, 1)
	raw := fixtures.RTPPackets(fixtures.RTPOptions{
		SampleCounts: []int64{1000, 1000, 1000}, Start: 0xFFFFF000, FrameBytes: 2,
	})
	packets := decode(t, raw)
	packets[1].Timestamp = 0x00000100 // wrap 1
	packets[2].Timestamp = 0xFFFFF800 // wrap 2
	_, err := Reconcile(packets, rate, 1, 2, DefaultLimits())
	if fault.CodeOf(err) != fault.CodeRTPOverflow {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestReconcileRejectsPacketOverLimit(t *testing.T) {
	rate := clock.NewRate(8000, 1)
	raw := fixtures.RTPPackets(fixtures.RTPOptions{SampleCounts: []int64{10, 10}, FrameBytes: 2})
	limits := DefaultLimits()
	limits.MaxPackets = 1
	_, err := Reconcile(decode(t, raw), rate, 8000, 2, limits)
	if fault.CodeOf(err) != fault.CodeLimit {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func mustMarshal(p *rtp.Packet) []byte {
	b, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}
