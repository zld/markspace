package runner

import (
	"context"
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"

	"relaydiag/internal/signal"
	"relaydiag/internal/transport"
)

// candidateBag collects trickled ICE candidates of one peer, guarding each
// against the loopback-only policy.
type candidateBag struct {
	mu    sync.Mutex
	items []signal.ICEPayload
	err   error
}

func (b *candidateBag) watch(p *transport.Peer) {
	p.PC.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if err := transport.GuardCandidate(c); err != nil {
			b.mu.Lock()
			b.err = err
			b.mu.Unlock()
			return
		}
		init := c.ToJSON()
		pl := signal.ICEPayload{Candidate: init.Candidate}
		if init.SDPMid != nil {
			pl.SDPMid = *init.SDPMid
		}
		if init.SDPMLineIndex != nil {
			pl.SDPMLineIndex = *init.SDPMLineIndex
		}
		b.mu.Lock()
		b.items = append(b.items, pl)
		b.mu.Unlock()
	})
}

func (b *candidateBag) snapshot() ([]signal.ICEPayload, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]signal.ICEPayload(nil), b.items...), b.err
}

// dial runs the full local signaling exchange for one connection and blocks
// until ICE is connected on both ends.
func dial(ctx context.Context, offerer, answerer *transport.Peer, label string) error {
	sigA := signal.NewEndpoint(label+"-offerer", signal.Offerer)
	sigB := signal.NewEndpoint(label+"-answerer", signal.Answerer)
	defer sigA.Close()
	defer sigB.Close()

	bagA := &candidateBag{}
	bagA.watch(offerer)
	bagB := &candidateBag{}
	bagB.watch(answerer)

	offer, err := offerer.PC.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("dial[%s]: create offer: %w", label, err)
	}
	if err := offerer.PC.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("dial[%s]: set local offer: %w", label, err)
	}
	if err := waitGather(ctx, offerer.PC); err != nil {
		return fmt.Errorf("dial[%s]: %w", label, err)
	}
	if err := exchangeSDP(sigA, sigB, signal.TypeOffer, offerer.PC.LocalDescription().SDP, answerer.PC); err != nil {
		return fmt.Errorf("dial[%s]: %w", label, err)
	}

	answer, err := answerer.PC.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("dial[%s]: create answer: %w", label, err)
	}
	if err := answerer.PC.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("dial[%s]: set local answer: %w", label, err)
	}
	if err := waitGather(ctx, answerer.PC); err != nil {
		return fmt.Errorf("dial[%s]: %w", label, err)
	}
	if err := exchangeSDP(sigB, sigA, signal.TypeAnswer, answerer.PC.LocalDescription().SDP, offerer.PC); err != nil {
		return fmt.Errorf("dial[%s]: %w", label, err)
	}

	if err := exchangeICE(sigA, sigB, bagA, answerer.PC, label); err != nil {
		return err
	}
	if err := exchangeICE(sigB, sigA, bagB, offerer.PC, label); err != nil {
		return err
	}

	if err := offerer.WaitConnected(ctx); err != nil {
		return err
	}
	if err := answerer.WaitConnected(ctx); err != nil {
		return err
	}
	if err := sigA.MarkReady(); err != nil {
		return err
	}
	return sigB.MarkReady()
}

// deliver encodes payload, decodes it back (exercising the codec on every
// message) and feeds the envelope to both endpoints' state machines.
func deliver(sigFrom, sigTo *signal.Endpoint, mt signal.MessageType, payload any) (*signal.Envelope, error) {
	data, err := signal.Encode(mt, payload)
	if err != nil {
		return nil, err
	}
	env, err := signal.Decode(data)
	if err != nil {
		return nil, err
	}
	if err := sigFrom.HandleMessage(env); err != nil {
		return nil, err
	}
	if err := sigTo.HandleMessage(env); err != nil {
		return nil, err
	}
	return env, nil
}

func exchangeSDP(sigFrom, sigTo *signal.Endpoint, mt signal.MessageType, sdp string, remote *webrtc.PeerConnection) error {
	kind := "offer"
	descType := webrtc.SDPTypeOffer
	if mt == signal.TypeAnswer {
		kind = "answer"
		descType = webrtc.SDPTypeAnswer
	}
	env, err := deliver(sigFrom, sigTo, mt, signal.SDPPayload{SDP: sdp, Type: kind})
	if err != nil {
		return err
	}
	var p signal.SDPPayload
	if err := signal.DecodePayload(env, &p); err != nil {
		return err
	}
	if err := remote.SetRemoteDescription(webrtc.SessionDescription{Type: descType, SDP: p.SDP}); err != nil {
		return fmt.Errorf("set remote %s: %w", kind, err)
	}
	return nil
}

func exchangeICE(sigFrom, sigTo *signal.Endpoint, bag *candidateBag, remote *webrtc.PeerConnection, label string) error {
	items, err := bag.snapshot()
	if err != nil {
		return fmt.Errorf("dial[%s]: %w", label, err)
	}
	for _, pl := range items {
		if _, err := deliver(sigFrom, sigTo, signal.TypeICE, pl); err != nil {
			return fmt.Errorf("dial[%s]: %w", label, err)
		}
		init := webrtc.ICECandidateInit{Candidate: pl.Candidate}
		if pl.SDPMid != "" {
			mid := pl.SDPMid
			init.SDPMid = &mid
		}
		idx := pl.SDPMLineIndex
		init.SDPMLineIndex = &idx
		if err := remote.AddICECandidate(init); err != nil {
			return fmt.Errorf("dial[%s]: add ICE candidate: %w", label, err)
		}
	}
	// End-of-candidates marker moves both endpoints to StateReady.
	if _, err := deliver(sigFrom, sigTo, signal.TypeICE, signal.ICEPayload{}); err != nil {
		return fmt.Errorf("dial[%s]: %w", label, err)
	}
	return nil
}

func waitGather(ctx context.Context, pc *webrtc.PeerConnection) error {
	select {
	case <-webrtc.GatheringCompletePromise(pc):
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ICE gathering: %w", ctx.Err())
	}
}
