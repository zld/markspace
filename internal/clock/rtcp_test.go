package clock

import (
	"testing"

	"example.com/mediaforge/internal/merrors"
)

func TestParseSenderReportRoundTrip(t *testing.T) {
	sr := NewSenderReport(0x11223344, 1_700_000_000_500_000, 90000)
	raw := BuildSenderReport(sr)
	got, err := ParseSenderReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.SSRC != sr.SSRC || got.RTPTime != sr.RTPTime {
		t.Fatalf("got %+v want %+v", got, sr)
	}
	// NTP fraction round-trips within 1 microsecond.
	diff := int64(got.NTPMicros()) - int64(sr.NTPMicros())
	if diff < -1 || diff > 1 {
		t.Fatalf("ntp micros = %d, want %d", got.NTPMicros(), sr.NTPMicros())
	}
}

func TestParseSenderReportSkipsOtherPackets(t *testing.T) {
	// Receiver Report (PT 201) first, then the SR.
	rr := make([]byte, 8)
	rr[0] = 0x80
	rr[1] = 201
	rr[3] = 1 // length 2 words
	sr := BuildSenderReport(NewSenderReport(7, 1_000_000, 42))
	raw := append(rr, sr...)
	got, err := ParseSenderReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.SSRC != 7 {
		t.Fatalf("ssrc = %d", got.SSRC)
	}
}

func TestParseSenderReportErrors(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		cat  merrors.Category
	}{
		{"short", []byte{0x80}, merrors.Truncated},
		{"no sr", []byte{0x80, 201, 0, 1, 0, 0, 0, 1}, merrors.Malformed},
		{"overrun", []byte{0x80, 200, 0, 100, 0, 0, 0, 0}, merrors.Truncated},
		{"truncated sr", []byte{0x80, 200, 0, 6, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, merrors.Truncated},
		{"oversize", make([]byte, MaxRTCPSize+1), merrors.Bounds},
	}
	for _, tc := range cases {
		_, err := ParseSenderReport(tc.buf)
		if err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
		if cat, ok := merrors.CategoryOf(err); !ok || cat != tc.cat {
			t.Fatalf("%s: category = %v (%v)", tc.name, cat, err)
		}
		var me *merrors.Error
		if !asError(err, &me) || !me.HasOff {
			t.Fatalf("%s: error lacks offset: %v", tc.name, err)
		}
	}
}

func asError(err error, target **merrors.Error) bool {
	for err != nil {
		if me, ok := err.(*merrors.Error); ok {
			*target = me
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestDriftEstimatorSteady(t *testing.T) {
	const rate = 90000
	est, err := NewDriftEstimator(rate)
	if err != nil {
		t.Fatal(err)
	}
	// Device runs 120 ppm fast: over 1s of NTP it emits 90000 + 10.8 ticks.
	baseNTP := uint64(1_700_000_000_000_000)
	baseRTP := uint64(4_000_000_000)
	if _, err := est.Observe(NewSenderReport(1, baseNTP, uint32(baseRTP)), baseRTP); err != nil {
		t.Fatal(err)
	}
	var last int64
	for i := uint64(1); i <= 20; i++ {
		ntp := baseNTP + i*1_000_000
		rtp := baseRTP + i*(rate+rate*120/1_000_000)
		last, err = est.Observe(NewSenderReport(1, ntp, uint32(rtp)), rtp)
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	if last < 100 || last > 140 {
		t.Fatalf("ppm = %d, want 100..140", last)
	}
	if est.Isolated() {
		t.Fatal("unexpected isolation")
	}
}

func TestDriftEstimatorQuarantinesGlitch(t *testing.T) {
	const rate = 90000
	est, _ := NewDriftEstimator(rate)
	baseNTP := uint64(1_700_000_000_000_000)
	baseRTP := uint64(100000)
	if _, err := est.Observe(NewSenderReport(1, baseNTP, uint32(baseRTP)), baseRTP); err != nil {
		t.Fatal(err)
	}
	// Establish a good baseline.
	ntp1 := baseNTP + 1_000_000
	rtp1 := baseRTP + rate
	ppm, err := est.Observe(NewSenderReport(1, ntp1, uint32(rtp1)), rtp1)
	if err != nil {
		t.Fatal(err)
	}
	if ppm != 0 {
		t.Fatalf("ppm = %d", ppm)
	}

	// Now a report whose RTP clock jumped half a second ahead in 1ms.
	glitchNTP := ntp1 + 1_000
	glitchRTP := rtp1 + rate/2
	_, err = est.Observe(NewSenderReport(1, glitchNTP, uint32(glitchRTP)), glitchRTP)
	if !merrors.IsCategory(err, merrors.OutOfRange) {
		t.Fatalf("glitch err = %v", err)
	}
	if !est.Isolated() {
		t.Fatal("estimator did not isolate")
	}
	if got, _ := est.PPM(); got != 0 {
		t.Fatalf("committed estimate polluted: %d", got)
	}

	// The next good report relative to the old baseline is fine again, but
	// with the glitched sample as baseline it would look wrong. A single
	// extra glitched report re-anchors; verify that path explicitly.
	_, err = est.Observe(NewSenderReport(1, glitchNTP+1_000_000, uint32(glitchRTP+rate)), glitchRTP+rate)
	if err != nil {
		t.Fatalf("re-anchor failed: %v", err)
	}
	if est.Isolated() {
		t.Fatal("still isolated after re-anchor")
	}
	if got, _ := est.PPM(); got != 0 {
		t.Fatalf("post re-anchor ppm = %d", got)
	}
}

func TestDriftEstimatorRejectsNonAdvancing(t *testing.T) {
	est, _ := NewDriftEstimator(48000)
	base := uint64(2_000_000_000)
	if _, err := est.Observe(NewSenderReport(1, base, 1000), 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := est.Observe(NewSenderReport(1, base, 1000), 1000); err == nil {
		t.Fatal("expected non-advancing error")
	}
	if _, err := est.Observe(NewSenderReport(1, base-1, 999), 999); err == nil {
		t.Fatal("expected backwards error")
	}
	accepted, rejected := est.Samples()
	if accepted != 0 || rejected != 2 {
		t.Fatalf("samples accepted=%d rejected=%d", accepted, rejected)
	}
}

func TestNewDriftEstimatorValidates(t *testing.T) {
	if _, err := NewDriftEstimator(0); err == nil {
		t.Fatal("expected error")
	}
}
