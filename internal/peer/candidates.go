// Package peer wires Pion PeerConnections to the loopback signaling server
// and the bounded RTP relay, enforcing loopback-only ICE gathering.
package peer

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/pion/webrtc/v4"

	"relaylab/internal/stats"
)

// CandidateCounters tracks how gathering treats each candidate address.
type CandidateCounters struct {
	AcceptedLoopback int64
	RejectedNonLoop  int64
	RejectedMdns     int64
	RejectedIPv6     int64
	RejectedNonUDP   int64
}

// IsLoopbackOnly is the single source of truth for address admission. It
// accepts only 127.0.0.0/8 IPv4 loopback; IPv6 loopback, mDNS names and
// every other address are rejected.
func IsLoopbackOnly(addr string) bool {
	if strings.Contains(addr, ".local") {
		return false
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		// Hostname or otherwise unparseable: never admit.
		return false
	}
	if ip.To4() == nil {
		return false // IPv6 (including ::1) is rejected per experiment policy
	}
	return ip.IsLoopback()
}

// IPFilter is the SettingEngine filter: only loopback IPv4 survives.
func IPFilter(c *CandidateCounters) func(net.IP) bool {
	return func(ip net.IP) bool {
		if ip == nil {
			atomic.AddInt64(&c.RejectedNonLoop, 1)
			return false
		}
		if ip.To4() == nil {
			atomic.AddInt64(&c.RejectedIPv6, 1)
			return false
		}
		if !ip.IsLoopback() {
			atomic.AddInt64(&c.RejectedNonLoop, 1)
			return false
		}
		atomic.AddInt64(&c.AcceptedLoopback, 1)
		return true
	}
}

// FilterCandidate applies the admission policy to a Pion ICE candidate and
// records why it was rejected. Returns true when the candidate may be used.
func FilterCandidate(c *CandidateCounters, cand *webrtc.ICECandidate) bool {
	if cand == nil {
		return false
	}
	addr := cand.Address
	switch {
	case strings.Contains(addr, ".local"):
		atomic.AddInt64(&c.RejectedMdns, 1)
		return false
	case cand.Protocol != webrtc.ICEProtocolUDP:
		atomic.AddInt64(&c.RejectedNonUDP, 1)
		return false
	default:
		if IsLoopbackOnly(addr) {
			atomic.AddInt64(&c.AcceptedLoopback, 1)
			return true
		}
		if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
			atomic.AddInt64(&c.RejectedIPv6, 1)
		} else {
			atomic.AddInt64(&c.RejectedNonLoop, 1)
		}
		return false
	}
}

// Snapshot returns the counters by value.
func (c *CandidateCounters) Snapshot() CandidateCounters {
	return CandidateCounters{
		AcceptedLoopback: atomic.LoadInt64(&c.AcceptedLoopback),
		RejectedNonLoop:  atomic.LoadInt64(&c.RejectedNonLoop),
		RejectedMdns:     atomic.LoadInt64(&c.RejectedMdns),
		RejectedIPv6:     atomic.LoadInt64(&c.RejectedIPv6),
		RejectedNonUDP:   atomic.LoadInt64(&c.RejectedNonUDP),
	}
}

// CollectCandidatePairs walks a PeerConnection stats report and returns the
// succeeded candidate pairs, asserting every address is loopback. A pair
// containing a non-loopback address is dropped and reported via the error.
func CollectCandidatePairs(report webrtc.StatsReport) ([]stats.CandidatePair, error) {
	candidates := map[string]webrtc.ICECandidateStats{}
	for _, s := range report {
		if cs, ok := s.(webrtc.ICECandidateStats); ok {
			if cs.Type == webrtc.StatsTypeLocalCandidate || cs.Type == webrtc.StatsTypeRemoteCandidate {
				candidates[cs.ID] = cs
			}
		}
	}
	out := []stats.CandidatePair{}
	for _, s := range report {
		pair, ok := s.(webrtc.ICECandidatePairStats)
		if !ok || pair.Type != webrtc.StatsTypeCandidatePair {
			continue
		}
		if pair.State != webrtc.StatsICECandidatePairStateSucceeded {
			continue
		}
		local, lok := candidates[pair.LocalCandidateID]
		remote, rok := candidates[pair.RemoteCandidateID]
		if !lok || !rok {
			continue
		}
		if !IsLoopbackOnly(local.IP) || !IsLoopbackOnly(remote.IP) {
			return nil, fmt.Errorf("non-loopback address in succeeded pair: %s <-> %s", local.IP, remote.IP)
		}
		out = append(out, stats.CandidatePair{
			LocalAddr:  joinHostPort(local.IP, local.Port),
			RemoteAddr: joinHostPort(remote.IP, remote.Port),
			Protocol:   local.Protocol,
			Type:       local.CandidateType.String(),
		})
	}
	return out, nil
}

func joinHostPort(ip string, port int32) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	return netip.AddrPortFrom(addr, uint16(port)).String()
}
