// Package transport wraps Pion PeerConnection setup with loopback-only ICE.
package transport

import (
	"context"
	"fmt"
	"net"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// Peer is one end of a loopback WebRTC connection.
type Peer struct {
	Name string
	PC   *webrtc.PeerConnection
}

// newAPI builds a WebRTC API restricted to loopback interfaces so no
// non-local candidate can ever be gathered.
func newAPI() *webrtc.API {
	se := webrtc.SettingEngine{}
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeUDP4,
		webrtc.NetworkTypeUDP6,
	})
	// Advertise real loopback IPs instead of mDNS names so candidate
	// validation can inspect addresses directly.
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	return webrtc.NewAPI(webrtc.WithSettingEngine(se))
}

// NewPeer creates a PeerConnection with a single audio transceiver direction
// set by the caller via tracks. The configuration uses no ICE servers: only
// host candidates on loopback are gathered.
func NewPeer(name string) (*Peer, error) {
	pc, err := newAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("transport[%s]: create peer connection: %w", name, err)
	}
	return &Peer{Name: name, PC: pc}, nil
}

// Close shuts the PeerConnection down.
func (p *Peer) Close() error {
	if p.PC == nil {
		return nil
	}
	if err := p.PC.Close(); err != nil {
		return fmt.Errorf("transport[%s]: close: %w", p.Name, err)
	}
	return nil
}

// WaitConnected blocks until the ICE connection reaches Connected/Completed,
// or fails on Failed/Disconnected/Closed or context expiry.
func (p *Peer) WaitConnected(ctx context.Context) error {
	done := make(chan error, 1)
	check := func(state webrtc.ICEConnectionState) {
		var err error
		switch state {
		case webrtc.ICEConnectionStateConnected, webrtc.ICEConnectionStateCompleted:
		case webrtc.ICEConnectionStateFailed:
			err = fmt.Errorf("ICE connection failed")
		case webrtc.ICEConnectionStateDisconnected, webrtc.ICEConnectionStateClosed:
			err = fmt.Errorf("ICE connection %s before connected", state)
		default:
			return
		}
		select {
		case done <- err:
		default:
		}
	}
	p.PC.OnICEConnectionStateChange(check)
	check(p.PC.ICEConnectionState()) // the state may already be terminal
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("transport[%s]: %w", p.Name, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("transport[%s]: waiting for ICE connected: %w", p.Name, ctx.Err())
	}
}
