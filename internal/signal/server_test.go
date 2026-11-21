package signal

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func startServer(t *testing.T) (*Server, *Client) {
	t.Helper()
	srv, err := NewServer("127.0.0.1:0", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve()
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		wg.Wait()
	})
	return srv, NewClient(srv.BaseURL(), time.Second)
}

func TestServerRejectsNonLoopbackListener(t *testing.T) {
	if _, err := NewServer("192.0.2.1:0", time.Second); err == nil {
		t.Fatal("expected non-loopback bind to fail")
	}
	if err := requireLoopback(&net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 9000}); err == nil {
		t.Fatal("expected non-loopback addr rejection")
	}
	if err := requireLoopback(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9000}); err != nil {
		t.Fatal(err)
	}
}

func TestServerRoundTrip(t *testing.T) {
	srv, client := startServer(t)
	if !strings.HasPrefix(srv.BaseURL(), "http://127.0.0.1:") {
		t.Fatalf("base url=%s", srv.BaseURL())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	opened, err := client.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if opened.RequestID == 0 {
		t.Fatal("missing request id")
	}
	if err := client.PutOffer(ctx, opened.RequestID, LinkIngress, "offer-ingress"); err != nil {
		t.Fatal(err)
	}
	if err := client.PutAnswer(ctx, opened.RequestID, LinkIngress, "answer-ingress"); err != nil {
		t.Fatal(err)
	}

	offer, err := srv.Store().AwaitOffer(ctx, opened.RequestID, LinkIngress)
	if err != nil || offer != "offer-ingress" {
		t.Fatalf("offer=%q err=%v", offer, err)
	}
	answer, err := srv.Store().AwaitAnswer(ctx, opened.RequestID, LinkIngress)
	if err != nil || answer != "answer-ingress" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}

	snap, err := client.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RequestID != opened.RequestID || snap.Closed {
		t.Fatalf("state=%+v", snap)
	}
	if snap.Links[0].Phase != PhaseAnswered || snap.Links[1].Phase != PhaseIdle {
		t.Fatalf("links=%+v", snap.Links)
	}
}

func TestServerErrorCodes(t *testing.T) {
	_, client := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Answer before offer is out of order and terminates the round.
	round, err := client.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PutAnswer(ctx, round.RequestID, LinkEgress, "answer"); err == nil {
		t.Fatal("expected out-of-order error")
	} else {
		assertCode(t, err, "out_of_order", 409)
	}
	snap, err := client.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Closed || snap.Links[1].Phase != PhaseClosed {
		t.Fatalf("round should be closed after protocol error: %+v", snap)
	}

	// Identical offer repeated is a duplicate and terminates the round.
	round, err = client.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PutOffer(ctx, round.RequestID, LinkEgress, "offer"); err != nil {
		t.Fatal(err)
	}
	if err := client.PutOffer(ctx, round.RequestID, LinkEgress, "offer"); err == nil {
		t.Fatal("expected duplicate error")
	} else {
		assertCode(t, err, "duplicate_request", 409)
	}

	// Unknown links are rejected before touching state.
	round, err = client.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PutOffer(ctx, round.RequestID, "sideways", "offer"); err == nil {
		t.Fatal("expected invalid link error")
	} else {
		assertCode(t, err, "invalid_link", 400)
	}

	// A superseded round reports expired.
	stale := round.RequestID
	if _, err := client.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.PutOffer(ctx, stale, LinkIngress, "stale"); err == nil {
		t.Fatal("expected expired error")
	} else {
		assertCode(t, err, "expired_request", 410)
	}
}

func TestServerShutdownClosesRound(t *testing.T) {
	srv, client := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	opened, err := client.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	snap := srv.Store().Snapshot()
	if snap == nil || !snap.Closed {
		t.Fatalf("round should be closed on shutdown: %+v", snap)
	}
	if _, err := srv.Store().AwaitOffer(ctx, opened.RequestID, LinkIngress); !errors.Is(err, ErrAborted) {
		t.Fatalf("waiter err=%v", err)
	}
}

func assertCode(t *testing.T, err error, code string, status int) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiErr.Code != code || apiErr.Status != status {
		t.Fatalf("got code=%s status=%d, want %s/%d", apiErr.Code, apiErr.Status, code, status)
	}
}
