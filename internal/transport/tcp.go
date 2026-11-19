package transport

import (
	"sync"

	"github.com/bluenviron/gortsplib/v4"
)

// tcpAdapter drives RTP/RTCP over the single RTSP control connection
// using the interleaved-framing transport. No extra sockets are
// opened, so there is no port range to manage.
type tcpAdapter struct {
	client  *gortsplib.Client
	mu      sync.Mutex
	closed  bool
	started bool
}

func newTCPAdapter(cfg Config) *tcpAdapter {
	client := newBaseClient(cfg)
	transport := gortsplib.TransportTCP
	client.Transport = &transport
	return &tcpAdapter{client: client}
}

func (a *tcpAdapter) Mode() Mode                { return ModeTCP }
func (a *tcpAdapter) Client() *gortsplib.Client { return a.client }
func (a *tcpAdapter) OnRTP(h RawHandler)        { a.client.OnPacketRTPAny(wireCallback(h)) }
func (a *tcpAdapter) MarkStarted() {
	a.mu.Lock()
	a.started = true
	a.mu.Unlock()
}

// Close shuts down the control connection and its interleaved
// channels. Repeated calls are no-ops so failure-path cleanup never
// double-closes. A client that never started is skipped because
// gortsplib's Close would dereference a nil context.
func (a *tcpAdapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.started {
		a.client.Close()
	}
	return nil
}

// New constructs the adapter selected by cfg.Mode.
func New(cfg Config) (Adapter, error) {
	switch cfg.Mode {
	case ModeTCP:
		return newTCPAdapter(cfg), nil
	case ModeUDP:
		return newUDPAdapter(cfg)
	default:
		return nil, ErrInvalidMode
	}
}
