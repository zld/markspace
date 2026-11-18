package peer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// ErrNotConnected is returned when media IO is attempted before ICE connects.
var ErrNotConnected = errors.New("peer connection not established")

// Peer wraps one Pion PeerConnection with loopback-only gathering and the
// single static audio track used by the experiment.
type Peer struct {
	name      string
	pc        *webrtc.PeerConnection
	track     *webrtc.TrackLocalStaticRTP
	counters  *CandidateCounters

	mu       sync.Mutex
	connected bool
	connCh   chan struct{}
	closed   bool

	remoteMu      sync.Mutex
	remoteTrack   *webrtc.TrackRemote
	remoteTrackCh chan struct{}

	gatherDone chan struct{}
	closeOnce  sync.Once
}

// NewLoopbackAPI builds a Pion API whose SettingEngine gathers only loopback
// IPv4 UDP host candidates with mDNS disabled.
func NewLoopbackAPI(counters *CandidateCounters) (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypePCMU,
			ClockRate: 8000,
		},
		PayloadType: 0,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register codec: %w", err)
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, fmt.Errorf("interceptors: %w", err)
	}
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(IPFilter(counters))
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetInterfaceFilter(func(name string) bool { return name == "lo0" })
	return webrtc.NewAPI(webrtc.WithMediaEngine(m),
		webrtc.WithInterceptorRegistry(ir),
		webrtc.WithSettingEngine(se)), nil
}

// New constructs a Peer and wires the connection/gathering/track callbacks.
func New(name string, api *webrtc.API, counters *CandidateCounters) (*Peer, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypePCMU,
		ClockRate: 8000,
	}, "audio-"+name, "relaylab")
	if err != nil {
		return nil, fmt.Errorf("create track: %w", err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("new peer connection: %w", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("add track: %w", err)
	}
	p := &Peer{
		name:          name,
		pc:            pc,
		track:         track,
		counters:      counters,
		connCh:        make(chan struct{}),
		remoteTrackCh: make(chan struct{}),
		gatherDone:    make(chan struct{}),
	}
	pc.OnICEConnectionStateChange(p.onICEState)
	pc.OnICEGatheringStateChange(func(state webrtc.ICEGatheringState) {
		if state == webrtc.ICEGatheringStateComplete {
			select {
			case <-p.gatherDone:
			default:
				close(p.gatherDone)
			}
		}
	})
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		p.remoteMu.Lock()
		p.remoteTrack = t
		p.remoteMu.Unlock()
		select {
		case <-p.remoteTrackCh:
		default:
			close(p.remoteTrackCh)
		}
	})
	return p, nil
}

func (p *Peer) onICEState(state webrtc.ICEConnectionState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if state == webrtc.ICEConnectionStateConnected ||
		state == webrtc.ICEConnectionStateCompleted {
		if !p.connected {
			p.connected = true
			close(p.connCh)
		}
	}
}

// OnCandidate registers the trickle-ICE sink; a nil candidate means end of
// gathering (gatherDone channel also tracks this internally).
func (p *Peer) OnCandidate(fn func(*webrtc.ICECandidate)) {
	p.pc.OnICECandidate(fn)
}

// CreateOffer runs the offer half of negotiation.
func (p *Peer) CreateOffer(ctx context.Context) (webrtc.SessionDescription, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("create offer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("set local: %w", err)
	}
	if err := p.waitGathered(ctx); err != nil {
		return webrtc.SessionDescription{}, err
	}
	return *p.pc.LocalDescription(), nil
}

// CreateAnswer runs the answer half of negotiation.
func (p *Peer) CreateAnswer(ctx context.Context, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if err := p.pc.SetRemoteDescription(offer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("set remote offer: %w", err)
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("create answer: %w", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("set local: %w", err)
	}
	if err := p.waitGathered(ctx); err != nil {
		return webrtc.SessionDescription{}, err
	}
	return *p.pc.LocalDescription(), nil
}

// SetRemoteAnswer applies the negotiated answer on the offering peer.
func (p *Peer) SetRemoteAnswer(answer webrtc.SessionDescription) error {
	if err := p.pc.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("set remote answer: %w", err)
	}
	return nil
}

// AddCandidate applies one trickle candidate after filtering.
func (p *Peer) AddCandidate(ctx context.Context, init webrtc.ICECandidateInit) error {
	if err := p.pc.AddICECandidate(init); err != nil {
		return fmt.Errorf("add ice candidate: %w", err)
	}
	return nil
}

func (p *Peer) waitGathered(ctx context.Context) error {
	select {
	case <-p.gatherDone:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ICE gathering timed out: %w", ctx.Err())
	}
}

// WaitConnected blocks until ICE reaches connected/completed or ctx expires.
func (p *Peer) WaitConnected(ctx context.Context) error {
	select {
	case <-p.connCh:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ICE connection timed out: %w", ctx.Err())
	}
}

// WaitRemoteTrack blocks until the remote track is announced.
func (p *Peer) WaitRemoteTrack(ctx context.Context) error {
	select {
	case <-p.remoteTrackCh:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for remote track: %w", ctx.Err())
	}
}

// WriteRTP sends one packet on the local track without altering its fields.
func (p *Peer) WriteRTP(pkt *rtp.Packet) error {
	p.mu.Lock()
	ok := p.connected
	p.mu.Unlock()
	if !ok {
		return ErrNotConnected
	}
	return p.track.WriteRTP(pkt)
}

// ReadRTP blocks for one packet from the remote track.
func (p *Peer) ReadRTP() (*rtp.Packet, error) {
	p.remoteMu.Lock()
	t := p.remoteTrack
	p.remoteMu.Unlock()
	if t == nil {
		return nil, ErrNotConnected
	}
	pkt, _, err := t.ReadRTP()
	return pkt, err
}

// Stats exposes the underlying Pion stats report.
func (p *Peer) Stats() webrtc.StatsReport { return p.pc.GetStats() }

// Close releases the PeerConnection; it is idempotent and never blocks.
func (p *Peer) Close() error {
	var err error
	p.closeOnce.Do(func() { err = p.pc.Close() })
	return err
}

// Name reports the peer label.
func (p *Peer) Name() string { return p.name }
