package transport

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/pion/rtp"
)

// Adapter is implemented by both the UDP and the TCP-interleaved media
// paths. The gortsplib server handler talks only to this interface, which is
// what lets one state machine and one pipeline implementation serve both
// transports while keeping separate counters.
type Adapter interface {
	// Name is the stable label printed in the JSON report.
	Name() string
	// OnSetup validates the transport negotiated during SETUP.
	OnSetup(t *gortsplib.Transport) error
	// OnPacket accepts every RTP packet delivered to the session.
	OnPacket(pkt *rtp.Packet)
	// Pipeline returns the counters and the reassembled frames.
	Pipeline() *Pipeline
	// Close flushes buffered data on session teardown.
	Close()
}

// UDPAdapter is the media-path adapter for RTP carried in UDP datagrams.
type UDPAdapter struct {
	pipeline *Pipeline
	active   bool
}

// NewUDPAdapter creates a UDP adapter with a fresh pipeline.
func NewUDPAdapter() *UDPAdapter {
	return &UDPAdapter{pipeline: NewPipeline()}
}

// Name returns the stable transport label used in reports.
func (a *UDPAdapter) Name() string { return "UDP" }

// OnSetup validates the negotiated transport for this adapter.
func (a *UDPAdapter) OnSetup(t *gortsplib.Transport) error {
	if t == nil {
		return fmt.Errorf("transport: UDP adapter received an empty SETUP transport")
	}
	if *t != gortsplib.TransportUDP {
		return fmt.Errorf("transport: expected UDP, negotiated %s", t)
	}
	a.active = true
	return nil
}

// OnPacket feeds a server-delivered RTP packet into the shared pipeline.
func (a *UDPAdapter) OnPacket(pkt *rtp.Packet) {
	a.pipeline.HandlePacket(pkt)
}

// Pipeline exposes the shared pipeline to the server handler.
func (a *UDPAdapter) Pipeline() *Pipeline { return a.pipeline }

// Close flushes the pipeline when the session tears down.
func (a *UDPAdapter) Close() { a.pipeline.Close() }
