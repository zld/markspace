package peer

import (
	"net"
	"testing"

	"github.com/pion/webrtc/v4"
)

func TestIsLoopbackOnly(t *testing.T) {
	accept := []string{"127.0.0.1", "127.1.2.3", "127.255.255.254"}
	reject := []string{
		"::1", "fe80::1", "2001:db8::1", // IPv6 incl. loopback
		"192.168.1.5", "10.0.0.2", "169.254.1.1", // non-loop IPv4
		"host.local", "example.com", "",
	}
	for _, a := range accept {
		if !IsLoopbackOnly(a) {
			t.Errorf("address %s should be admitted", a)
		}
	}
	for _, a := range reject {
		if IsLoopbackOnly(a) {
			t.Errorf("address %s must be rejected", a)
		}
	}
}

func TestIPFilter(t *testing.T) {
	c := &CandidateCounters{}
	f := IPFilter(c)
	if !f(net.ParseIP("127.0.0.1")) {
		t.Error("loopback rejected")
	}
	for _, ip := range []net.IP{net.ParseIP("::1"), net.ParseIP("192.168.0.1"), nil} {
		if f(ip) {
			t.Errorf("IP %v admitted", ip)
		}
	}
	snap := c.Snapshot()
	if snap.AcceptedLoopback != 1 || snap.RejectedIPv6 != 1 || snap.RejectedNonLoop != 2 {
		t.Fatalf("unexpected counters: %+v", snap)
	}
}

func TestFilterCandidate(t *testing.T) {
	c := &CandidateCounters{}
	udp := webrtc.ICEProtocolUDP
	cases := []struct {
		name string
		cand *webrtc.ICECandidate
		want bool
	}{
		{"loopback", &webrtc.ICECandidate{Address: "127.0.0.1", Protocol: udp, Typ: webrtc.ICECandidateTypeHost}, true},
		{"mdns", &webrtc.ICECandidate{Address: "abc.local", Protocol: udp}, false},
		{"ipv6", &webrtc.ICECandidate{Address: "::1", Protocol: udp}, false},
		{"lan", &webrtc.ICECandidate{Address: "10.0.0.9", Protocol: udp}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := FilterCandidate(c, tc.cand); got != tc.want {
			t.Errorf("case %s: got %v want %v", tc.name, got, tc.want)
		}
	}
	snap := c.Snapshot()
	if snap.AcceptedLoopback != 1 || snap.RejectedMdns != 1 ||
		snap.RejectedIPv6 != 1 || snap.RejectedNonLoop != 1 {
		t.Fatalf("unexpected counters: %+v", snap)
	}
}

func TestCollectCandidatePairsRejectsExternal(t *testing.T) {
	// A succeeded pair whose remote IP is a LAN address must be refused.
	report := webrtc.StatsReport{
		"l": webrtc.ICECandidateStats{
			Timestamp: 0, Type: webrtc.StatsTypeLocalCandidate, ID: "l",
			IP: "127.0.0.1", Port: 10000, Protocol: "udp", CandidateType: webrtc.ICECandidateTypeHost,
		},
		"r": webrtc.ICECandidateStats{
			Timestamp: 0, Type: webrtc.StatsTypeRemoteCandidate, ID: "r",
			IP: "192.168.1.20", Port: 20000, Protocol: "udp", CandidateType: webrtc.ICECandidateTypeHost,
		},
		"p": webrtc.ICECandidatePairStats{
			Type: webrtc.StatsTypeCandidatePair, ID: "p",
			LocalCandidateID: "l", RemoteCandidateID: "r",
			State: webrtc.StatsICECandidatePairStateSucceeded,
		},
	}
	if _, err := CollectCandidatePairs(report); err == nil {
		t.Fatal("external address in candidate pair must be rejected")
	}

	report["r"] = webrtc.ICECandidateStats{
		Timestamp: 0, Type: webrtc.StatsTypeRemoteCandidate, ID: "r",
		IP: "127.0.0.1", Port: 20000, Protocol: "udp", CandidateType: webrtc.ICECandidateTypeHost,
	}
	pairs, err := CollectCandidatePairs(report)
	if err != nil {
		t.Fatalf("loopback pair rejected: %v", err)
	}
	if len(pairs) != 1 || pairs[0].Type != "host" || pairs[0].Protocol != "udp" {
		t.Fatalf("unexpected pairs: %+v", pairs)
	}
}
