package main

import (
	"os"
	"time"

	"github.com/pion/webrtc/v4"

	"relaycheck/internal/metrics"
	"relaycheck/internal/relay"
)

// countCandidatePairs counts ICE candidate-pair stats entries on the
// connection (host-host pairs on loopback; typically 1-4).
func countCandidatePairs(pc *webrtc.PeerConnection) int {
	n := 0
	for _, s := range pc.GetStats() {
		if _, ok := s.(webrtc.ICECandidatePairStats); ok {
			n++
		}
	}
	return n
}

func buildReport(fwd *relay.Forwarder, pairs, rejected int, start time.Time, clockRate uint32) metrics.Report {
	st := fwd.Stats()
	j := fwd.Jitter()
	return metrics.Report{
		CandidatePairs:     pairs,
		RejectedCandidates: rejected,
		ForwardedPackets:   st.Forwarded,
		DroppedPackets:     st.Dropped,
		DuplicatePackets:   st.Duplicates,
		ReorderedPackets:   st.Reordered,
		WriteErrors:        st.WriteErrs,
		JitterMs:           j.Milliseconds(),
		JitterSamples:      j.Samples(),
		JitterValid:        j.Valid(),
		DurationMs:         time.Since(start).Milliseconds(),
	}
}

func emitReport(p runParams, r metrics.Report) {
	if p.jsonOut {
		r.WriteJSON(os.Stdout)
	} else {
		r.WriteSummary(os.Stdout)
	}
}
