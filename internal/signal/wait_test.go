package signal

import (
	"context"
	"testing"
	"time"
)

func TestServerLongPollDelivery(t *testing.T) {
	_, client := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opened, err := client.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}

	offerCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		sdp, err := client.AwaitOffer(ctx, opened.RequestID, LinkIngress)
		if err != nil {
			errCh <- err
			return
		}
		offerCh <- sdp
	}()

	time.Sleep(50 * time.Millisecond)
	if err := client.PutOffer(ctx, opened.RequestID, LinkIngress, "sdp-offer"); err != nil {
		t.Fatal(err)
	}
	select {
	case sdp := <-offerCh:
		if sdp != "sdp-offer" {
			t.Fatalf("offer=%q", sdp)
		}
	case err := <-errCh:
		t.Fatalf("await offer: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("long poll did not return")
	}

	if err := client.PutAnswer(ctx, opened.RequestID, LinkIngress, "sdp-answer"); err != nil {
		t.Fatal(err)
	}
	answer, err := client.AwaitAnswer(ctx, opened.RequestID, LinkIngress)
	if err != nil || answer != "sdp-answer" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}
