package signal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOfferAnswerHappyPath(t *testing.T) {
	s := NewStore(time.Second)
	id, _ := s.Open()
	if err := s.PutOffer(id, LinkIngress, "v=0 offer"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := s.AwaitOffer(ctx, id, LinkIngress)
	if err != nil || got != "v=0 offer" {
		t.Fatalf("offer=%q err=%v", got, err)
	}
	if err := s.PutAnswer(id, LinkIngress, "v=0 answer"); err != nil {
		t.Fatal(err)
	}
	got, err = s.AwaitAnswer(ctx, id, LinkIngress)
	if err != nil || got != "v=0 answer" {
		t.Fatalf("answer=%q err=%v", got, err)
	}
}

func TestDuplicateAndOutOfOrder(t *testing.T) {
	s := NewStore(time.Second)
	id, _ := s.Open()
	if err := s.PutAnswer(id, LinkEgress, "ans"); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("answer before offer: %v", err)
	}
	id, _ = s.Open()
	if err := s.PutOffer(id, LinkEgress, "offer"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOffer(id, LinkEgress, "offer"); !errors.Is(err, ErrDuplicateRequest) {
		t.Fatalf("duplicate offer: %v", err)
	}
	id, _ = s.Open()
	if err := s.PutOffer(id, LinkIngress, "offer"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAnswer(id, LinkIngress, "answer"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOffer(id, LinkIngress, "offer"); !errors.Is(err, ErrDuplicateRequest) {
		t.Fatalf("duplicate offer after answer: %v", err)
	}
}

func TestStaleRequestRejected(t *testing.T) {
	s := NewStore(time.Second)
	first, _ := s.Open()
	second, _ := s.Open()
	if err := s.PutOffer(first, LinkIngress, "old"); !errors.Is(err, ErrExpiredRequest) {
		t.Fatalf("stale offer: %v", err)
	}
	if err := s.PutOffer(second, LinkIngress, "new"); err != nil {
		t.Fatalf("current offer: %v", err)
	}
	if err := s.PutOffer(999, LinkIngress, "future"); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestNegotiationTimeoutWakesWaiters(t *testing.T) {
	s := NewStore(50 * time.Millisecond)
	id, _ := s.Open()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := s.AwaitOffer(ctx, id, LinkEgress)
	if !errors.Is(err, ErrNegotiationTimeout) {
		t.Fatalf("await err=%v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout was not prompt: %s", elapsed)
	}
	if err := s.PutOffer(id, LinkEgress, "late"); !errors.Is(err, ErrNegotiationTimeout) {
		t.Fatalf("late offer err=%v", err)
	}
}

func TestAbortReleasesWaiters(t *testing.T) {
	s := NewStore(time.Minute)
	id, _ := s.Open()
	done := make(chan error, 1)
	go func() {
		_, err := s.AwaitOffer(context.Background(), id, LinkIngress)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := s.Abort(id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrAborted) {
			t.Fatalf("waiter err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter not released")
	}
	if err := s.Abort(12345); !errors.Is(err, ErrUnknownRequest) {
		t.Fatalf("abort unknown: %v", err)
	}
}

func TestSnapshotReflectsPhases(t *testing.T) {
	s := NewStore(time.Minute)
	id, _ := s.Open()
	if err := s.PutOffer(id, LinkIngress, "offer"); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if snap == nil || snap.RequestID != id {
		t.Fatalf("snapshot=%+v", snap)
	}
	if len(snap.Links) != 2 {
		t.Fatalf("links=%d", len(snap.Links))
	}
	if snap.Links[0].Link != LinkIngress || snap.Links[0].Phase != PhaseOffered {
		t.Fatalf("ingress=%+v", snap.Links[0])
	}
	if snap.Links[1].Phase != PhaseIdle {
		t.Fatalf("egress=%+v", snap.Links[1])
	}
}
