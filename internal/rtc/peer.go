package rtc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// Negotiation errors mapped to the signaling/ICE exit code by the caller.
var (
	ErrGatheringTimeout = errors.New("ICE gathering timeout")
	ErrConnectTimeout   = errors.New("ICE connection timeout")
	ErrConnectFailed    = errors.New("ICE connection failed")
	ErrNoSelectedPair   = errors.New("no selected ICE candidate pair")
	ErrIncompleteLink   = errors.New("link did not reach a complete candidate pair")
)

// Pair is the selected candidate pair for one link, with loopback addresses.
type Pair struct {
	LocalAddress  string `json:"local_address"`
	LocalPort     uint16 `json:"local_port"`
	RemoteAddress string `json:"remote_address"`
	RemotePort    uint16 `json:"remote_port"`
}

// String renders the pair for terminal output.
func (p Pair) String() string {
	return fmt.Sprintf("%s:%d -> %s:%d", p.LocalAddress, p.LocalPort, p.RemoteAddress, p.RemotePort)
}

// Peer wraps a Pion PeerConnection with loopback enforcement.
type Peer struct {
	name   string
	pc     *webrtc.PeerConnection
	policy Policy

	mu          sync.Mutex
	localErr    error
	nonLoopback []string
	closed      bool
}

// NewPeer creates a loopback-only PeerConnection. No media track is attached;
// callers add tracks or transceivers before negotiating.
func NewPeer(name string, policy Policy) (*Peer, error) {
	api := NewAPI(policy)
	pc, err := api.NewPeerConnection(configuration())
	if err != nil {
		return nil, fmt.Errorf("%s: new peer connection: %w", name, err)
	}
	p := &Peer{name: name, pc: pc, policy: policy}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if err := ValidateCandidate(name, c); err != nil {
			p.mu.Lock()
			if p.localErr == nil {
				p.localErr = err
			}
			p.nonLoopback = append(p.nonLoopback, c.Address)
			p.mu.Unlock()
		}
	})
	return p, nil
}

// PC exposes the underlying connection for track wiring.
func (p *Peer) PC() *webrtc.PeerConnection { return p.pc }

// Name returns the peer's logical name.
func (p *Peer) Name() string { return p.name }

// WaitConnected blocks until DTLS/ICE are connected or the context ends.
func (p *Peer) WaitConnected(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		switch state := p.pc.ConnectionState(); state {
		case webrtc.PeerConnectionStateConnected:
			return nil
		case webrtc.PeerConnectionStateFailed:
			return fmt.Errorf("%w: %s is %s", ErrConnectFailed, p.name, state)
		case webrtc.PeerConnectionStateClosed:
			return fmt.Errorf("%w: %s is %s", ErrConnectFailed, p.name, state)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %s: %v", ErrConnectTimeout, p.name, ctx.Err())
		case <-ticker.C:
		}
	}
}

// CheckCandidates fails if any gathered candidate was not loopback.
func (p *Peer) CheckCandidates() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.localErr != nil {
		return p.localErr
	}
	return nil
}
