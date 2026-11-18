package transport

import (
	"fmt"
	"net"

	"github.com/pion/webrtc/v4"
)

// PairSummary reports the candidate-pair view of one PeerConnection.
type PairSummary struct {
	Total         int    // number of candidate pairs known to ICE
	SelectedState string // state of the selected pair, "" if none
}

// IsLoopbackAddr reports whether addr is a loopback IP (127.0.0.1 or ::1).
func IsLoopbackAddr(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

// GuardCandidate rejects any gathered candidate that is not loopback.
// A nil candidate (end-of-candidates) is accepted.
func GuardCandidate(c *webrtc.ICECandidate) error {
	if c == nil {
		return nil
	}
	if !IsLoopbackAddr(c.Address) {
		return fmt.Errorf("non-loopback candidate %s (%s) gathered; terminating", c.Address, c.Typ)
	}
	return nil
}

// CollectPairs inspects the connection statistics and returns the total
// number of candidate pairs and the state of the selected pair.
func CollectPairs(pc *webrtc.PeerConnection) PairSummary {
	report := pc.GetStats()

	selectedID := ""
	for _, s := range report {
		if t, ok := s.(webrtc.TransportStats); ok && t.SelectedCandidatePairID != "" {
			selectedID = t.SelectedCandidatePairID
			break
		}
	}

	sum := PairSummary{}
	nominatedState := ""
	for _, s := range report {
		pair, ok := s.(webrtc.ICECandidatePairStats)
		if !ok {
			continue
		}
		sum.Total++
		if pair.ID == selectedID {
			sum.SelectedState = string(pair.State)
		}
		if pair.Nominated {
			nominatedState = string(pair.State)
		}
	}
	// Pion does not populate SelectedCandidatePairID; the nominated pair
	// is the selected one per RFC 8445.
	if sum.SelectedState == "" {
		sum.SelectedState = nominatedState
	}
	return sum
}
