package ts

import (
	"testing"

	"rtpregress/internal/clock"
)

func TestPCRWrapExpandsOnlyWithContinuity(t *testing.T) {
	// place PCR near the end of the 42-bit range, then step across the wrap
	nearWrap := clock.PCRWrapTicks - clock.PCRRate/5 // 200 ms before wrap
	pkts := psiSetup()
	pkts = append(pkts,
		pcrPacket(testPCRPID, 0, nearWrap, false),
		pcrPacket(testPCRPID, 1, nearWrap+clock.PCRRate/5, false), // wraps once
	)
	_, st, err := collect(pkts, PolicyStrict)
	if err != nil {
		t.Fatalf("a continuous wrap must not error: %v", err)
	}
	if st.PCRWraps != 1 || st.PCRSeen != 2 {
		t.Fatalf("PCR stats wrong: %+v", st)
	}
}

func TestPCRBackwardJumpFailsStrictAndIsolates(t *testing.T) {
	pkts := psiSetup()
	pkts = append(pkts,
		pcrPacket(testPCRPID, 0, clock.PCRRate*10, false),
		pcrPacket(testPCRPID, 1, clock.PCRRate, false), // 9 s backwards, no wrap
	)
	if _, _, err := collect(pkts, PolicyStrict); !isCode(err, clock.CodePCRDiscont) {
		t.Fatalf("strict must reject PCR jump: %v", err)
	}
	_, st, err := collect(pkts, PolicyIsolate)
	if err != nil || st.Gaps == 0 {
		t.Fatalf("isolate must swallow PCR jump: err=%v stats=%+v", err, st)
	}
}

func TestDriftEstimationFromRTPAnchors(t *testing.T) {
	d := NewDemuxer(bytesReader(nil), PolicyStrict)
	// RTP and PCR agree exactly: zero drift
	for i := int64(1); i <= 5; i++ {
		ns := i * clock.NanosPerSecond / 10
		d.AnchorRTP(uint32(clock.NanosToRTPTicks(ns)), 0)
		// inject a matching PCR via the estimator path
		d.drift.Add(ns, ns)
	}
	if d.Stats().DriftPPM != 0 {
		t.Fatalf("matched clocks must show 0 ppm, got %d", d.Stats().DriftPPM)
	}
}

func TestPTS33BitUnwrap(t *testing.T) {
	u := &pts33Unwrapper{}
	high := pts33Mod - 90000 // 1 s before wrap
	a := u.Unwrap(high)
	b := u.Unwrap(90000) // wraps to 1 s past the period boundary
	if b <= a {
		t.Fatalf("PTS must stay monotonic across 33-bit wrap: %d -> %d", a, b)
	}
	if b-a != 180000 {
		t.Fatalf("wrap step = %d ticks, want 180000", b-a)
	}
}

func TestPolicyParsing(t *testing.T) {
	if p, ok := ParsePolicy("strict"); !ok || p != PolicyStrict {
		t.Fatal("strict policy parse")
	}
	if p, ok := ParsePolicy("isolate"); !ok || p != PolicyIsolate {
		t.Fatal("isolate policy parse")
	}
	if _, ok := ParsePolicy("bogus"); ok {
		t.Fatal("bogus policy must fail")
	}
}

func TestIsAudioStreamType(t *testing.T) {
	for _, st := range []uint8{0x03, 0x04, 0x0F, 0x11, 0x81} {
		if !IsAudioStreamType(st) {
			t.Errorf("stream type 0x%x is audio", st)
		}
	}
	if IsAudioStreamType(0x1B) { // H.264 video
		t.Error("video stream type misclassified as audio")
	}
}
