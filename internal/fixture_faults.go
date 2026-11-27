package rtpreplay

import (
	"math/rand"
)

// generateMalformedOpus produces one packet with an empty payload (no TOC
// byte) in the middle of an otherwise valid stream. The parser must return a
// structured error naming the track and sequence number.
func generateMalformedOpus(seed int64) (*Fixture, error) {
	rng := rand.New(rand.NewSource(seed))
	const n = 20
	tr := simpleTrace(seed, uint64(n*20))
	for i := 0; i < n; i++ {
		var payload []byte
		if i == 12 {
			payload = nil // missing TOC byte
		} else {
			payload = opusFrame20ms(rng, 50)
		}
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    rtpHex(uint16(i+1), uint32(i*960), false, payload, 111),
			ArrivalMs: int64(baseDelayMs + i*20),
			Fault:     faultAt(i == 12, "malformed-opus"),
		})
	}
	fx := &Fixture{Case: CaseMalformedOpus, Seed: seed, Trace: tr}
	fx.Notes = append(fx.Notes, "seq 13 carries an empty Opus payload; duration is underivable and must fail the run with a structured error")
	return fx, nil
}

// generateTimestampJump keeps sequence numbers contiguous but advances the RTP
// timestamp by a value inconsistent with the AU duration at one packet,
// simulating a sender-clock discontinuity that must not be silently absorbed.
func generateTimestampJump(seed int64) (*Fixture, error) {
	rng := rand.New(rand.NewSource(seed))
	const n = 20
	tr := simpleTrace(seed, uint64(n*20))
	for i := 0; i < n; i++ {
		ts := uint32(i * 960)
		if i >= 13 {
			ts += 7000 // discontinuous jump
		}
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    rtpHex(uint16(i+1), ts, false, opusFrame20ms(rng, 50), 111),
			ArrivalMs: int64(baseDelayMs + i*20),
			Fault:     faultAt(i == 13, "timestamp-jump"),
		})
	}
	fx := &Fixture{Case: CaseTimestampJump, Seed: seed, Trace: tr}
	fx.Notes = append(fx.Notes, "seq 14 jumps the RTP timestamp by 7960 ticks where 960 is expected")
	return fx, nil
}

// generateBlockedDag keeps the input valid but marks a mid-pipeline node to
// fail with a retryable-then-fatal pattern, so the DAG must report the first
// blocked node rather than a false success.
func generateBlockedDag(seed int64) (*Fixture, error) {
	fx, err := generateNormal(seed)
	if err != nil {
		return nil, err
	}
	fx.Case = CaseBlockedDag
	fx.Notes = append(fx.Notes,
		"valid media input; the metrics node is configured to fail so the DAG must report its first blocked node")
	return fx, nil
}

func faultAt(cond bool, name string) string {
	if cond {
		return name
	}
	return ""
}
