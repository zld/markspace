package peer

import (
	"context"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// negotiate stands up two loopback peers and runs full offer/answer with
// non-trickled (gather-complete) SDP, then confirms a connected ICE pair.
func negotiate(t *testing.T) (*Peer, *Peer, *CandidateCounters) {
	t.Helper()
	ca, cb := &CandidateCounters{}, &CandidateCounters{}
	apiA, err := NewLoopbackAPI(ca)
	if err != nil {
		t.Fatal(err)
	}
	apiB, err := NewLoopbackAPI(cb)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New("A", apiA, ca)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New("B", apiB, cb)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	offer, err := a.CreateOffer(ctx)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	answer, err := b.CreateAnswer(ctx, offer)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if err := a.SetRemoteAnswer(answer); err != nil {
		t.Fatalf("remote answer: %v", err)
	}
	if err := a.WaitConnected(ctx); err != nil {
		t.Fatalf("A connect: %v", err)
	}
	if err := b.WaitConnected(ctx); err != nil {
		t.Fatalf("B connect: %v", err)
	}
	return a, b, ca
}

func TestLoopbackNegotiation(t *testing.T) {
	a, b, counters := negotiate(t)

	pairs, err := CollectCandidatePairs(a.Stats())
	if err != nil {
		t.Fatalf("candidate pairs: %v", err)
	}
	if len(pairs) == 0 {
		t.Fatal("expected at least one succeeded candidate pair")
	}
	for _, p := range pairs {
		if !IsLoopbackOnly(hostOnly(p.LocalAddr)) || !IsLoopbackOnly(hostOnly(p.RemoteAddr)) {
			t.Fatalf("non-loopback pair leaked: %+v", p)
		}
		if p.Protocol != "udp" || p.Type != "host" {
			t.Fatalf("pair is not udp/host: %+v", p)
		}
	}
	snap := counters.Snapshot()
	if snap.AcceptedLoopback == 0 {
		t.Fatal("no loopback candidate was accepted during gathering")
	}
	if snap.RejectedIPv6 != 0 || snap.RejectedNonLoop != 0 {
		t.Fatalf("unexpected rejections on loopback-only host: %+v", snap)
	}
	if a.Name() != "A" || b.Name() != "B" {
		t.Fatal("peer labels wrong")
	}
}

func TestTrickleCandidatePath(t *testing.T) {
	// Reuse the gather-complete SDP but exercise AddICECandidate with an
	// explicit loopback candidate init on each peer.
	a, b, _ := negotiate(t)
	ctx := context.Background()
	init := webrtc.ICECandidateInit{
		Candidate: "candidate:9 1 udp 2130706431 127.0.0.1 49999 typ host",
	}
	if err := a.AddCandidate(ctx, init); err != nil {
		t.Fatalf("trickle add on A: %v", err)
	}
	if err := b.AddCandidate(ctx, init); err != nil {
		t.Fatalf("trickle add on B: %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	c := &CandidateCounters{}
	api, _ := NewLoopbackAPI(c)
	p, err := New("C", api, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second close must be safe: %v", err)
	}
}

// hostOnly strips the :port suffix produced by AddrPort formatting.
func hostOnly(addrPort string) string {
	for i := len(addrPort) - 1; i >= 0; i-- {
		if addrPort[i] == ':' {
			return addrPort[:i]
		}
	}
	return addrPort
}
