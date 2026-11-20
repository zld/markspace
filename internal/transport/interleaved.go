package transport

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/pion/rtp"
)

// InterleavedAdapter is the media-path adapter for RTP multiplexed over the
// RTSP TCP control connection (RTP-over-RTSP, interleaved on channel 0).
type InterleavedAdapter struct {
	pipeline *Pipeline
	active   bool
}

// NewInterleavedAdapter creates a TCP-interleaved adapter with a fresh
// pipeline, fully independent of any UDP adapter running in the same process.
func NewInterleavedAdapter() *InterleavedAdapter {
	return &InterleavedAdapter{pipeline: NewPipeline()}
}

// Name returns the stable transport label used in reports.
func (a *InterleavedAdapter) Name() string { return "TCP" }

// OnSetup validates the negotiated transport for this adapter.
func (a *InterleavedAdapter) OnSetup(t *gortsplib.Transport) error {
	if t == nil {
		return fmt.Errorf("transport: TCP adapter received an empty SETUP transport")
	}
	if *t != gortsplib.TransportTCP {
		return fmt.Errorf("transport: expected TCP interleaved, negotiated %s", t)
	}
	a.active = true
	return nil
}

// OnPacket feeds a server-delivered RTP packet into the shared pipeline.
func (a *InterleavedAdapter) OnPacket(pkt *rtp.Packet) {
	a.pipeline.HandlePacket(pkt)
}

// Pipeline exposes the shared pipeline to the server handler.
func (a *InterleavedAdapter) Pipeline() *Pipeline { return a.pipeline }

// Close flushes the pipeline when the session tears down.
func (a *InterleavedAdapter) Close() { a.pipeline.Close() }
