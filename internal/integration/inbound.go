package integration

import (
	"context"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	relaypkg "looprelay/internal/relay"
)

// inboundForwarder reads the ingress track and hands packets to the bounded
// relay queue.
type inboundForwarder struct {
	fwd *relaypkg.Forwarder

	mu       sync.Mutex
	received int
}

func newInboundForwarder(fwd *relaypkg.Forwarder) *inboundForwarder {
	return &inboundForwarder{fwd: fwd}
}

func (f *inboundForwarder) onTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	go func() {
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			clone := *pkt
			clone.Payload = append([]byte(nil), pkt.Payload...)
			_ = f.fwd.Receive(&clone)
			f.mu.Lock()
			f.received++
			f.mu.Unlock()
		}
	}()
}

// awaitCount waits for count arrivals or until the deadline/context ends.
func (f *inboundForwarder) awaitCount(ctx context.Context, count int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f.count() >= count {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// count reports how many packets have arrived at the ingress hop.
func (f *inboundForwarder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.received
}
