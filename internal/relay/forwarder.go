package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
)

// Forwarder copies RTP packets from an inbound track of one connection to
// an outbound track of the other, verifying payload content per packet.
type Forwarder struct {
	dst       *webrtc.TrackLocalStaticRTP
	forwarded atomic.Uint64
	corrupted atomic.Uint64
}

// NewForwarder creates a Forwarder writing to dst.
func NewForwarder(dst *webrtc.TrackLocalStaticRTP) *Forwarder {
	return &Forwarder{dst: dst}
}

// Forwarded returns the number of packets successfully written downstream.
func (f *Forwarder) Forwarded() uint64 { return f.forwarded.Load() }

// Corrupted returns the number of packets whose payload did not match the
// expected deterministic content.
func (f *Forwarder) Corrupted() uint64 { return f.corrupted.Load() }

// Run reads packets from src until the track ends, ctx expires or a write
// fails. A normal end-of-track (peer closed after all packets) returns nil.
func (f *Forwarder) Run(ctx context.Context, src *webrtc.TrackRemote) error {
	for {
		pkt, _, err := src.ReadRTP()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("relay: read from inbound track: %w", err)
		}
		if !bytes.Equal(pkt.Payload, ExpectedPayload(pkt.SequenceNumber, len(pkt.Payload))) {
			f.corrupted.Add(1)
		}
		if err := f.dst.WriteRTP(pkt); err != nil {
			if errors.Is(err, io.ErrClosedPipe) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("relay: write to outbound track: %w", err)
		}
		f.forwarded.Add(1)
	}
}
