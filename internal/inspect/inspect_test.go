package inspect

import (
	"testing"
	"time"

	"github.com/pion/rtp"

	"mediadag/internal/clock"
	"mediadag/internal/ingest"
)

func anchoredTimeline() *clock.Timeline {
	tl := clock.NewTimeline()
	tl.AddSenderReport(0, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return tl
}

func ev(ts uint32, payload []byte, marker bool) ingest.Event {
	return ingest.Event{
		Kind: ingest.KindRTP,
		Packet: &rtp.Packet{
			Header:  rtp.Header{Timestamp: ts, Marker: marker},
			Payload: payload,
		},
	}
}

func singleNAL(nalType byte, size int) []byte {
	p := make([]byte, size)
	p[0] = 0x60 | nalType // F=0 NRI=3 type
	return p
}

func TestSingleNALClassification(t *testing.T) {
	a := NewAnalyzer(anchoredTimeline())
	a.Add(ev(0, singleNAL(nalSPS, 10), false))
	a.Add(ev(0, singleNAL(nalPPS, 4), false))
	a.Add(ev(0, singleNAL(nalIDR, 100), true))
	a.Add(ev(3000, singleNAL(nalSlice, 80), true))
	a.Add(ev(90000, singleNAL(nalIDR, 100), true)) // IDR at t=1000ms
	res := a.Finish()
	if res.SPSCount != 1 || res.PPSCount != 1 || res.IDRCount != 2 || res.SliceCount != 1 {
		t.Fatalf("counts: %s", res.Summary())
	}
	if len(res.KeyframeIntMs) != 1 || res.KeyframeIntMs[0] < 999 || res.KeyframeIntMs[0] > 1001 {
		t.Fatalf("keyframe interval = %v", res.KeyframeIntMs)
	}
	if len(res.Corrupt) != 0 {
		t.Fatalf("unexpected corrupt: %+v", res.Corrupt)
	}
	if res.TimeUncertain {
		t.Fatal("timeline is anchored, must be certain")
	}
}

func TestSTAPA(t *testing.T) {
	a := NewAnalyzer(anchoredTimeline())
	sps, pps := singleNAL(nalSPS, 5), singleNAL(nalPPS, 3)
	payload := []byte{0x60 | nalSTAPA, 0, byte(len(sps))}
	payload = append(payload, sps...)
	payload = append(payload, 0, byte(len(pps)))
	payload = append(payload, pps...)
	a.Add(ev(0, payload, false))
	res := a.Finish()
	if res.SPSCount != 1 || res.PPSCount != 1 {
		t.Fatalf("counts: %s", res.Summary())
	}
}

func TestTruncatedSTAPA(t *testing.T) {
	a := NewAnalyzer(anchoredTimeline())
	// declares 100-byte NAL but carries only 4
	payload := append([]byte{0x60 | nalSTAPA, 0, 100}, 1, 2, 3, 4)
	a.Add(ev(0, payload, false))
	res := a.Finish()
	if len(res.Corrupt) != 1 || res.Corrupt[0].Reason != "truncated-nal" {
		t.Fatalf("corrupt = %+v", res.Corrupt)
	}
}

func TestFUAReassembly(t *testing.T) {
	a := NewAnalyzer(anchoredTimeline())
	mk := func(start, end bool, nalType byte) []byte {
		h := byte(0)
		if start {
			h |= 0x80
		}
		if end {
			h |= 0x40
		}
		return []byte{0x60 | nalFUA, h | nalType, 0xAA, 0xBB}
	}
	a.Add(ev(0, mk(true, false, nalIDR), false))
	a.Add(ev(0, mk(false, false, nalIDR), false))
	a.Add(ev(0, mk(false, true, nalIDR), true))
	res := a.Finish()
	if res.IDRCount != 1 {
		t.Fatalf("idr = %d, want 1", res.IDRCount)
	}
	if len(res.Corrupt) != 0 {
		t.Fatalf("corrupt = %+v", res.Corrupt)
	}
}

func TestFUATruncatedByTimestampChange(t *testing.T) {
	a := NewAnalyzer(anchoredTimeline())
	start := []byte{0x60 | nalFUA, 0x80 | nalIDR, 0xAA}
	a.Add(ev(0, start, false))
	a.Add(ev(3000, singleNAL(nalSlice, 10), true)) // next frame: dangling FU
	res := a.Finish()
	if len(res.Corrupt) != 1 || res.Corrupt[0].Reason != "truncated-nal" {
		t.Fatalf("corrupt = %+v", res.Corrupt)
	}
	if res.Corrupt[0].StartMs != 0 {
		t.Fatalf("corrupt located at %v ms, want 0", res.Corrupt[0].StartMs)
	}
}

func TestFUAMissingFragment(t *testing.T) {
	a := NewAnalyzer(anchoredTimeline())
	// end fragment without a start
	end := []byte{0x60 | nalFUA, 0x40 | nalIDR, 0xAA}
	a.Add(ev(0, end, true))
	res := a.Finish()
	if len(res.Corrupt) != 1 || res.Corrupt[0].Reason != "missing-fragment" {
		t.Fatalf("corrupt = %+v", res.Corrupt)
	}
}

func TestLossGapLocatedInTime(t *testing.T) {
	a := NewAnalyzer(anchoredTimeline())
	a.Add(ev(0, singleNAL(nalIDR, 50), true))
	a.Add(ev(9000, singleNAL(nalSlice, 50), true)) // t=100ms
	a.AddLossGaps([]ingest.Gap{{From: 5, To: 8}}, func(g ingest.Gap) (uint32, bool) {
		return 9000, true // first packet after the gap
	})
	res := a.Finish()
	if len(res.Corrupt) != 1 || res.Corrupt[0].Reason != "packet-loss" {
		t.Fatalf("corrupt = %+v", res.Corrupt)
	}
	if res.Corrupt[0].StartMs < 99 || res.Corrupt[0].StartMs > 101 {
		t.Fatalf("loss located at %v ms, want ~100", res.Corrupt[0].StartMs)
	}
}

func TestUnanchoredMarkedUncertain(t *testing.T) {
	a := NewAnalyzer(clock.NewTimeline())
	a.Add(ev(0, singleNAL(nalIDR, 50), true))
	res := a.Finish()
	if !res.TimeUncertain {
		t.Fatal("expected TimeUncertain without anchor")
	}
}
