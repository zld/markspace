package clock

import "testing"

func TestRTCPRoundTrip(t *testing.T) {
	want := RTCPReport{SSRC: 1234, NTPNS: 1_700_000_000_500_000_000, RTPTimestamp: 812345}
	b := MarshalSenderReport(want)
	got, err := ParseSenderReport(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.SSRC != want.SSRC || got.RTPTimestamp != want.RTPTimestamp {
		t.Fatalf("bad report: %+v", got)
	}
	diff := got.NTPNS - want.NTPNS
	if diff < 0 {
		diff = -diff
	}
	if diff > 1 { // NTP fraction gives sub-nanosecond quantization
		t.Fatalf("NTP time mismatch: got %d want %d diff %d", got.NTPNS, want.NTPNS, diff)
	}
}

func TestParseRTCPRejectsTruncated(t *testing.T) {
	if _, err := ParseSenderReport([]byte{0x80, 200}); err == nil {
		t.Fatal("expected truncation error")
	}
}
