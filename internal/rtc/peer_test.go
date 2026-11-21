package rtc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":    true,
		"127.5.6.7":    true,
		"::1":          true,
		"[::1]":        true,
		"192.168.1.10": false,
		"10.0.0.1":     false,
		"0.0.0.0":      false,
		"":             false,
		"example.com":  false,
		"::":           false,
	}
	for host, want := range cases {
		if got := IsLoopbackHost(host); got != want {
			t.Fatalf("IsLoopbackHost(%q)=%v want %v", host, got, want)
		}
	}
}

func TestValidateSDPCandidates(t *testing.T) {
	ok := "v=0\r\na=candidate:1 1 udp 2122260223 127.0.0.1 40000 typ host\r\na=candidate:2 1 udp 2122260223 ::1 40001 typ host\r\n"
	if err := ValidateSDPCandidates("test", ok); err != nil {
		t.Fatalf("loopback SDP rejected: %v", err)
	}
	bad := "v=0\r\na=candidate:1 1 udp 2122260223 192.168.0.10 40000 typ host\r\n"
	err := ValidateSDPCandidates("test", bad)
	var target *ErrNonLoopbackCandidate
	if !errors.As(err, &target) {
		t.Fatalf("expected loopback violation, got %v", err)
	}
	if target.Address != "192.168.0.10" {
		t.Fatalf("address=%s", target.Address)
	}
}

func TestPeerNegotiatesOverLoopback(t *testing.T) {
	policy := DefaultPolicy()
	offerer, err := NewPeer("offerer", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer offerer.Close()
	answerer, err := NewPeer("answerer", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer answerer.Close()

	if _, err := offerer.PC().AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	offer, err := offerer.CreateOffer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(offer.SDP, "a=candidate:") {
		t.Fatal("offer SDP should carry gathered candidates")
	}
	answer, err := answerer.AcceptOffer(ctx, offer.SDP)
	if err != nil {
		t.Fatal(err)
	}
	if err := offerer.AcceptAnswer(answer.SDP); err != nil {
		t.Fatal(err)
	}
	if err := offerer.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}
	if err := answerer.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}

	for _, peer := range []*Peer{offerer, answerer} {
		pair, err := peer.SelectedPair()
		if err != nil {
			t.Fatalf("%s: %v", peer.Name(), err)
		}
		if !IsLoopbackHost(pair.LocalAddress) || !IsLoopbackHost(pair.RemoteAddress) {
			t.Fatalf("%s: pair escaped loopback: %+v", peer.Name(), pair)
		}
		if strings.Contains(pair.String(), "0.0.0.0") {
			t.Fatalf("%s: unspecified address in pair: %s", peer.Name(), pair.String())
		}
	}
}
