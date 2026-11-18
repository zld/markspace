package signaling

import (
	"context"
	"strings"
	"testing"
	"time"
)

const testSDP = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nm=audio 50000 RTP/AVP 0\r\na=ice-ufrag:abcd\r\na=ice-pwd:secret\r\n"

func TestServerRoundTripAndSeq(t *testing.T) {
	srv, err := NewServer()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if !strings.HasPrefix(srv.Addr(), "http://127.0.0.1") {
		t.Fatalf("server not bound to loopback: %s", srv.Addr())
	}
	ctx := context.Background()
	id, err := CreateSession(ctx, srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	a := NewClient(srv.Addr(), id, PeerA)
	b := NewClient(srv.Addr(), id, PeerB)

	if err := a.Send(ctx, &Message{Type: MsgOffer, SDP: testSDP}); err != nil {
		t.Fatalf("offer: %v", err)
	}
	// Replay a stale seq directly against the session: must be rejected
	// and counted as a duplicate (seq == expected-1).
	srv.mu.Lock()
	sess := srv.sessions[id]
	srv.mu.Unlock()
	replay := &Message{Type: MsgCandidate, From: PeerA, To: PeerB, Seq: 1, Candidate: "candidate:1 1 udp 2130706431 127.0.0.1 50001 typ host"}
	if err := sess.apply(replay); err == nil {
		t.Fatal("duplicate message must be rejected")
	}

	got, err := b.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != MsgOffer {
		t.Fatalf("expected single offer, got %+v", got)
	}
	if err := b.Send(ctx, &Message{Type: MsgAnswer, SDP: testSDP}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	// Answer arriving at A.
	got, err = a.Poll(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("answer poll: %v %v", got, err)
	}
	cand := "candidate:1 1 udp 2130706431 127.0.0.1 50000 typ host"
	if err := a.Send(ctx, &Message{Type: MsgCandidate, Candidate: cand}); err != nil {
		t.Fatalf("candidate: %v", err)
	}
	counters, ok := srv.Counters(id)
	if !ok {
		t.Fatal("session missing")
	}
	if counters.DuplicateMessages != 1 {
		t.Errorf("duplicate counter = %d, want 1", counters.DuplicateMessages)
	}
}

func TestServerRejectsInvalidCandidate(t *testing.T) {
	srv, _ := NewServer()
	ctx := context.Background()
	id, _ := CreateSession(ctx, srv.Addr())
	a := NewClient(srv.Addr(), id, PeerA)
	if err := a.Send(ctx, &Message{Type: MsgCandidate, Candidate: "garbage"}); err == nil {
		t.Fatal("invalid candidate should be rejected")
	}
	counters, _ := srv.Counters(id)
	if counters.InvalidMessages == 0 {
		t.Error("invalid message not counted")
	}
}
