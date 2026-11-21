// Package rtc builds Pion peer connections that are constrained to loopback.
package rtc

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// ErrNonLoopbackCandidate reports a candidate outside 127.0.0.0/8, ::1, or
// link-local zone-tagged loopback forms.
type ErrNonLoopbackCandidate struct {
	Where   string
	Address string
}

// Error implements the error interface.
func (e *ErrNonLoopbackCandidate) Error() string {
	return fmt.Sprintf("%s candidate address %q is not loopback", e.Where, e.Address)
}

// IsLoopbackIP reports whether ip is usable as a loopback ICE address.
func IsLoopbackIP(ip net.IP) bool {
	return ip != nil && ip.IsLoopback()
}

// IsLoopbackHost accepts numeric loopback hosts and bracketed IPv6 literals.
func IsLoopbackHost(host string) bool {
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "" {
		return false
	}
	return IsLoopbackIP(net.ParseIP(host))
}

// Policy is the loopback-only ICE configuration shared by both hops.
type Policy struct {
	// NetworkTypes restricts Pion to UDP4/UDP6 candidates.
	NetworkTypes []webrtc.NetworkType
	// ICETimeout bounds connectivity checks.
	ICETimeout time.Duration
}

// DefaultPolicy allows only UDP candidates and no STUN/TURN.
func DefaultPolicy() Policy {
	return Policy{
		NetworkTypes: []webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6},
		ICETimeout:   5 * time.Second,
	}
}

// NewAPI builds a Pion API that gathers loopback host candidates only,
// disables mDNS, and rejects any non-loopback interface IP.
func NewAPI(policy Policy) *webrtc.API {
	media := &webrtc.MediaEngine{}
	if err := media.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeVP8,
			ClockRate: 90000,
			RTCPFeedback: []webrtc.RTCPFeedback{
				{Type: "nack"},
				{Type: "nack", Parameter: "pli"},
			},
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(fmt.Sprintf("register VP8 codec: %v", err))
	}

	settings := webrtc.SettingEngine{}
	settings.SetNetworkTypes(policy.NetworkTypes)
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	settings.SetInterfaceFilter(func(name string) bool {
		// Drop obviously non-loopback interfaces early; the IP filter is the
		// authoritative gate.
		return !strings.HasPrefix(name, "utun") && !strings.HasPrefix(name, "awdl")
	})
	settings.SetIPFilter(IsLoopbackIP)
	if policy.ICETimeout > 0 {
		settings.SetICETimeouts(policy.ICETimeout, policy.ICETimeout*2, 2*time.Second)
	}

	return webrtc.NewAPI(
		webrtc.WithMediaEngine(media),
		webrtc.WithSettingEngine(settings),
	)
}

// configuration returns the loopback-only PeerConnection configuration.
// No STUN or TURN servers are configured by design.
func configuration() webrtc.Configuration {
	return webrtc.Configuration{ICEServers: nil}
}

// candidateLine matches candidate attributes in SDP.
var candidateLine = regexp.MustCompile(`(?m)^a=candidate:(\S+) (\d+) (\S+) (\d+) (\S+) (\d+) typ (\S+)`)

// ValidateSDPCandidates extracts every a=candidate line and fails on any
// address that is not loopback. sdp should be the full description text.
func ValidateSDPCandidates(where, sdp string) error {
	for _, m := range candidateLine.FindAllStringSubmatch(sdp, -1) {
		address := m[5]
		if !IsLoopbackHost(address) {
			return &ErrNonLoopbackCandidate{Where: where, Address: address}
		}
	}
	return nil
}

// ValidateCandidate fails when a locally gathered candidate is not loopback.
func ValidateCandidate(where string, c *webrtc.ICECandidate) error {
	if c == nil {
		return nil
	}
	if !IsLoopbackHost(c.Address) {
		return &ErrNonLoopbackCandidate{Where: where, Address: c.Address}
	}
	if c.Typ == webrtc.ICECandidateTypeSrflx || c.Typ == webrtc.ICECandidateTypeRelay {
		return &ErrNonLoopbackCandidate{Where: where, Address: c.Address}
	}
	return nil
}
