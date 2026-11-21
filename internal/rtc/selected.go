package rtc

import (
	"errors"
	"fmt"

	"github.com/pion/webrtc/v4"
)

// SelectedPair returns the nominated candidate pair, enforcing loopback.
func (p *Peer) SelectedPair() (Pair, error) {
	pair, err := p.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil {
		return Pair{}, fmt.Errorf("%s: selected pair: %w", p.name, err)
	}
	if pair == nil || pair.Local == nil || pair.Remote == nil {
		return Pair{}, fmt.Errorf("%w: %s", ErrNoSelectedPair, p.name)
	}
	if !IsLoopbackHost(pair.Local.Address) || !IsLoopbackHost(pair.Remote.Address) {
		return Pair{}, &ErrNonLoopbackCandidate{Where: p.name + "/selected", Address: pair.Local.Address + "," + pair.Remote.Address}
	}
	return Pair{
		LocalAddress:  pair.Local.Address,
		LocalPort:     pair.Local.Port,
		RemoteAddress: pair.Remote.Address,
		RemotePort:    pair.Remote.Port,
	}, nil
}

// Close releases the PeerConnection. It is safe to call repeatedly.
func (p *Peer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	if err := p.pc.Close(); err != nil && !errors.Is(err, webrtc.ErrConnectionClosed) {
		return fmt.Errorf("%s: close: %w", p.name, err)
	}
	return nil
}
