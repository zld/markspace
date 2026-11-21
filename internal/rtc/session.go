package rtc

import (
	"context"
	"fmt"

	"github.com/pion/webrtc/v4"
)

// CreateOffer builds and applies a local offer, then waits for gathering.
func (p *Peer) CreateOffer(ctx context.Context) (webrtc.SessionDescription, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("%s: create offer: %w", p.name, err)
	}
	done := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("%s: set local offer: %w", p.name, err)
	}
	if err := p.waitGathering(ctx, done); err != nil {
		return webrtc.SessionDescription{}, err
	}
	return p.localDescription("offer")
}

// AcceptOffer applies a remote offer and returns a local answer after gathering.
func (p *Peer) AcceptOffer(ctx context.Context, sdp string) (webrtc.SessionDescription, error) {
	if err := ValidateSDPCandidates(p.name+"/remote", sdp); err != nil {
		return webrtc.SessionDescription{}, err
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("%s: set remote offer: %w", p.name, err)
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("%s: create answer: %w", p.name, err)
	}
	done := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("%s: set local answer: %w", p.name, err)
	}
	if err := p.waitGathering(ctx, done); err != nil {
		return webrtc.SessionDescription{}, err
	}
	return p.localDescription("answer")
}

// AcceptAnswer applies a remote answer and starts connectivity checks.
func (p *Peer) AcceptAnswer(sdp string) error {
	if err := ValidateSDPCandidates(p.name+"/remote", sdp); err != nil {
		return err
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		return fmt.Errorf("%s: set remote answer: %w", p.name, err)
	}
	return nil
}

func (p *Peer) waitGathering(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("%w: %s", ErrGatheringTimeout, p.name)
	}
	if err := p.CheckCandidates(); err != nil {
		return err
	}
	return nil
}

func (p *Peer) localDescription(kind string) (webrtc.SessionDescription, error) {
	desc := p.pc.LocalDescription()
	if desc == nil || desc.SDP == "" {
		return webrtc.SessionDescription{}, fmt.Errorf("%s: empty local %s", p.name, kind)
	}
	if err := ValidateSDPCandidates(p.name+"/local", desc.SDP); err != nil {
		return webrtc.SessionDescription{}, err
	}
	if err := p.CheckCandidates(); err != nil {
		return webrtc.SessionDescription{}, err
	}
	return *desc, nil
}
