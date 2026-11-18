package signaling

import "testing"

func TestValidateSDP(t *testing.T) {
	good := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nm=audio 50000 RTP/AVP 0\r\na=ice-ufrag:abcd\r\na=ice-pwd:secret\r\n"
	if err := ValidateSDP(good, MsgOffer); err != nil {
		t.Fatalf("valid SDP rejected: %v", err)
	}
	for _, tc := range []struct{ name, sdp, kind string }{
		{"empty", "", MsgOffer},
		{"no version", "o=- 1 1\r\nm=audio 1 RTP/AVP 0\r\na=ice-ufrag:x\r\na=ice-pwd:y\r\n", MsgOffer},
		{"no media", "v=0\r\no=- 1 1\r\na=ice-ufrag:x\r\na=ice-pwd:y\r\n", MsgOffer},
		{"no ice", "v=0\r\no=- 1 1\r\nm=audio 1 RTP/AVP 0\r\n", MsgAnswer},
	} {
		if err := ValidateSDP(tc.sdp, tc.kind); err == nil {
			t.Errorf("case %s: expected rejection", tc.name)
		}
	}
}

func TestValidateCandidateLine(t *testing.T) {
	ok := "candidate:1 1 udp 2130706431 127.0.0.1 50000 typ host"
	if err := ValidateCandidateLine(ok); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
	for _, line := range []string{
		"",
		"a=ice-ufrag:x",
		"candidate:1 1 tcp 2130706431 127.0.0.1 50000 typ host",
		"candidate:1 1 udp 2130706431 127.0.0.1 50000",
		"candidate:1 1 udp 2130706431 host.local 50000 typ host",
	} {
		if err := ValidateCandidateLine(line); err == nil {
			t.Errorf("candidate %q should be rejected", line)
		}
	}
}

func TestMessageValidatePayload(t *testing.T) {
	sdp := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\nm=audio 1 RTP/AVP 0\r\na=ice-ufrag:x\r\na=ice-pwd:y\r\n"
	cases := []Message{
		{Type: MsgOffer, From: PeerA, To: PeerB, Seq: 1, SDP: sdp},
		{Type: MsgAnswer, From: PeerB, To: PeerA, Seq: 1, SDP: sdp},
		{Type: MsgCandidate, From: PeerA, To: PeerB, Seq: 2, Candidate: "candidate:1 1 udp 2130706431 127.0.0.1 50000 typ host"},
		{Type: MsgBye, From: PeerA, To: PeerB, Seq: 3},
	}
	for i, m := range cases {
		if err := m.ValidatePayload(); err != nil {
			t.Errorf("case %d unexpectedly rejected: %v", i, err)
		}
	}
	bad := []Message{
		{Type: MsgOffer, From: "C", To: PeerB, SDP: sdp},
		{Type: MsgOffer, From: PeerA, To: PeerA, SDP: sdp},
		{Type: "pong", From: PeerA, To: PeerB},
		{Type: MsgCandidate, From: PeerA, To: PeerB, Candidate: "garbage"},
	}
	for i, m := range bad {
		if err := m.ValidatePayload(); err == nil {
			t.Errorf("bad case %d should fail", i)
		}
	}
}
