package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"

	rtcpkg "looprelay/internal/rtc"
	"looprelay/internal/signal"
)

func vp8Capability() webrtc.RTPCodecCapability {
	return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
}

func negotiate(ctx context.Context, opts Options, client *signal.Client, requestID int64, source, ingress, egress, sink *rtcpkg.Peer) error {
	negCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	links := []struct {
		name      string
		offerer   *rtcpkg.Peer
		answerer  *rtcpkg.Peer
		signalKey string
	}{
		{"ingress", source, ingress, signal.LinkIngress},
		{"egress", egress, sink, signal.LinkEgress},
	}

	errCh := make(chan error, len(links))
	var wg sync.WaitGroup
	for _, l := range links {
		l := l
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := negotiateLink(negCtx, client, requestID, l.name, l.signalKey, l.offerer, l.answerer); err != nil {
				errCh <- err
				cancel()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	if err := negCtx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", signal.ErrNegotiationTimeout, err)
	}
	return nil
}

func negotiateLink(ctx context.Context, client *signal.Client, requestID int64, name, key string, offerer, answerer *rtcpkg.Peer) error {
	offer, err := offerer.CreateOffer(ctx)
	if err != nil {
		return fmt.Errorf("%s offer: %w", name, err)
	}
	if err := client.PutOffer(ctx, requestID, key, offer.SDP); err != nil {
		return fmt.Errorf("%s put offer: %w", name, err)
	}
	remoteOffer, err := client.AwaitOffer(ctx, requestID, key)
	if err != nil {
		return fmt.Errorf("%s await offer: %w", name, err)
	}
	answer, err := answerer.AcceptOffer(ctx, remoteOffer)
	if err != nil {
		return fmt.Errorf("%s accept offer: %w", name, err)
	}
	if err := client.PutAnswer(ctx, requestID, key, answer.SDP); err != nil {
		return fmt.Errorf("%s put answer: %w", name, err)
	}
	remoteAnswer, err := client.AwaitAnswer(ctx, requestID, key)
	if err != nil {
		return fmt.Errorf("%s await answer: %w", name, err)
	}
	if err := offerer.AcceptAnswer(remoteAnswer); err != nil {
		return fmt.Errorf("%s accept answer: %w", name, err)
	}
	return nil
}
