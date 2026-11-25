package rtp

import "testing"

func TestSenderReportRoundTrip(t *testing.T) {
	orig := SenderReport{
		SSRC:         4242,
		NTPSeconds:   3600.25,
		RTPTimestamp: 90000,
		PacketCount:  10,
		OctetCount:   1234,
	}
	buf := BuildSenderReport(orig)
	got, err := ParseCompoundRTCP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("report count = %d, want 1", len(got))
	}
	g := got[0]
	if g.SSRC != orig.SSRC || g.RTPTimestamp != orig.RTPTimestamp ||
		g.PacketCount != orig.PacketCount || g.OctetCount != orig.OctetCount {
		t.Fatalf("SR fields lost: %+v", g)
	}
	if d := g.NTPSeconds - orig.NTPSeconds; d > 1e-7 || d < -1e-7 {
		t.Fatalf("NTP seconds = %v, want %v", g.NTPSeconds, orig.NTPSeconds)
	}
}

func TestCompoundWithMultipleReports(t *testing.T) {
	a := BuildSenderReport(SenderReport{SSRC: 1, NTPSeconds: 10, RTPTimestamp: 100})
	b := BuildSenderReport(SenderReport{SSRC: 2, NTPSeconds: 20, RTPTimestamp: 200})
	reports, err := ParseCompoundRTCP(append(append([]byte{}, a...), b...))
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0].SSRC != 1 || reports[1].SSRC != 2 {
		t.Fatalf("compound parse wrong: %+v", reports)
	}
}

func TestMalformedRTCP(t *testing.T) {
	cases := [][]byte{
		{0x80, 200},                                  // truncated header
		{0x80, 200, 0x00, 0x02, 0x01, 0x02, 0x03},    // length overruns
		{0x40, 200, 0x00, 0x00},                      // bad version
		{0x80, 200, 0x00, 0x00, 0, 0, 0, 1},          // SR block too short
	}
	for i, c := range cases {
		if _, err := ParseCompoundRTCP(c); err == nil {
			t.Fatalf("case %d: malformed datagram accepted", i)
		}
	}
}
