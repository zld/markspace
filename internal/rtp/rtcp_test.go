package rtp

import (
	"testing"

	"mediasync/internal/clock"
)

// buildSR encodes a minimal RTCP Sender Report with no report blocks.
func buildSR(ssrc uint32, ntpSec, ntpFrac, rtpTS uint32) []byte {
	lengthWords := (srHeaderLen + srSenderInfoLen)/4 - 1 // length field excludes the 4-byte header base
	b := make([]byte, (lengthWords+1)*4)
	b[0] = 0x80 // version 2, padding 0, RC 0
	b[1] = rtcpSR
	b[2] = byte(lengthWords >> 8)
	b[3] = byte(lengthWords)
	putBE32(b[4:8], ssrc)
	putBE32(b[8:12], ntpSec)
	putBE32(b[12:16], ntpFrac)
	putBE32(b[16:20], rtpTS)
	return b
}

func putBE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func TestParseSenderReport(t *testing.T) {
	const ssrc uint32 = 4242
	// NTP time chosen so unix seconds are positive.
	sr, err := ParseSenderReport(buildSR(ssrc, ntpEpochOffset+10, 0, 90000), 11_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if sr.SSRC != ssrc || sr.RTPTime != 90000 {
		t.Fatalf("parsed SR fields wrong: %+v", sr)
	}
	if got := sr.WallNS(); got != 10_000_000_000 {
		t.Fatalf("wall time = %d want 10s", got)
	}
	if sr.ArrivalNS != 11_000_000_000 {
		t.Fatal("arrival time not preserved")
	}
}

func TestParseSRFractionScaling(t *testing.T) {
	// Half of one NTP second should map to ~500 ms.
	sr, err := ParseSenderReport(buildSR(1, ntpEpochOffset, 1<<31, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := sr.WallNS(); got < 499_000_000 || got > 501_000_000 {
		t.Fatalf("half-second fraction mapped to %d", got)
	}
}

func TestParseSRRejectsGarbage(t *testing.T) {
	if _, err := ParseSenderReport(nil, 0); err == nil {
		t.Fatal("nil RTCP accepted")
	}
	if _, err := ParseSenderReport([]byte{0x80, 200, 0x00, 0x09}, 0); err == nil {
		t.Fatal("truncated SR with large length accepted")
	}
	// A valid header for an RR (pt 201) must yield no SR.
	rr := []byte{0x80, 201, 0x00, 0x01, 0, 0, 0, 1}
	_, err2 := ParseSenderReport(rr, 0)
	if err2 == nil {
		t.Fatal("receiver report mistaken for sender report")
	}
	if ce, ok := clock.AsError(err2); !ok || ce.Kind != clock.ErrInvalidLength {
		t.Fatalf("error classification wrong: %v", err2)
	}
}

func TestSREstimatorIntegration(t *testing.T) {
	est := clock.NewDriftEstimator()
	for i := 1; i <= 4; i++ {
		rtpTS := uint32(i * 9000)
		sr, err := ParseSenderReport(buildSR(9, ntpEpochOffset+uint32(i), 0, rtpTS), 0)
		if err != nil {
			t.Fatal(err)
		}
		if e := est.Add(clock.SRPoint{RTP: int64(rtpTS), WallNS: sr.WallNS()}); e != nil {
			t.Fatal(e)
		}
	}
	if est.Len() != 4 {
		t.Fatal("estimator did not retain reports")
	}
	if _, ok := est.PPM(clock.DefaultClockRate); !ok {
		t.Fatal("drift estimate unavailable from SR series")
	}
}
