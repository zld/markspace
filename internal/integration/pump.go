package integration

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"

	rtpsource "looprelay/internal/rtp"
)

func pump(ctx context.Context, track *webrtc.TrackLocalStaticRTP, packets []rtpsource.Packet) error {
	if len(packets) == 0 {
		return nil
	}
	origin := time.Now()
	for _, p := range packets {
		if p.Skip {
			continue
		}
		wait := time.Until(origin.Add(p.SendAt.Sub(packets[0].SendAt)))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := track.WriteRTP(p.RTP); err != nil {
			return fmt.Errorf("source write seq %d: %w", p.RTP.SequenceNumber, err)
		}
	}
	return nil
}
