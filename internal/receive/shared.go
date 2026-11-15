// Package receive wires packet decoding, the reorder window, the shared
// jitter controller and packet loss concealment together. The offline replay
// path and the live UDP path use the same Player and therefore the same
// buffer control strategy.
package receive

import (
	"jbbench/internal/jitter"
	"jbbench/internal/metrics"
	"jbbench/internal/protocol"
)

// applySeqGate enforces the half-interval ambiguity rule before the Player's
// own modular comparison, keeping an independent invalid counter. It returns
// true when the packet must be rejected.
func applySeqGate(pkt *protocol.Packet, lastSeq uint16, hasSeq bool,
	drops *metrics.DropCounters) bool {
	if !hasSeq {
		return false
	}
	if _, amb := protocol.SeqCompare(pkt.Seq, lastSeq); amb {
		drops.Ambiguous++
		return true
	}
	return false
}

// recordOutcome translates one Player outcome into metrics. The curve level
// is the controller's target watermark in frames at play time.
func recordOutcome(c *metrics.Collector, p *jitter.Player, o jitter.Outcome) {
	c.RecordFrame(o.At, p.LevelFrames(), o.Latency, o.Concealed)
}
