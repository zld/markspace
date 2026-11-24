package fixture

import (
	"mediasync/internal/clock"
)

// applyLoss drops datagrams probabilistically, always keeping the first
// keyframe so a recoverable stream never starts inside a GOP.
func applyLoss(in []Datagram, r *rng, rate float64) []Datagram {
	if rate <= 0 {
		return in
	}
	out := make([]Datagram, 0, len(in))
	for i, dg := range in {
		if i > 0 && r.float64() < rate {
			continue
		}
		out = append(out, dg)
	}
	return out
}

// applyTruncation shortens a fraction of payloads to simulate structurally
// damaged samples that downstream recovery must isolate.
func applyTruncation(in []Datagram, r *rng, rate float64) []Datagram {
	if rate <= 0 {
		return in
	}
	for i := range in {
		if r.float64() >= rate {
			continue
		}
		dg := &in[i]
		if len(dg.Data) <= 12 {
			continue
		}
		cut := 12 + (len(dg.Data)-12)/3 // keep RTP header, drop 2/3 of payload
		dg.Data = append([]byte(nil), dg.Data[:cut]...)
		dg.Truncated = true
		dg.Corrupt = true
	}
	return in
}

// applyDuplicates repeats a fraction of datagrams while preserving order.
func applyDuplicates(in []Datagram, r *rng, rate float64) []Datagram {
	if rate <= 0 {
		return in
	}
	out := make([]Datagram, 0, len(in)+4)
	for _, dg := range in {
		out = append(out, dg)
		if r.float64() < rate {
			dup := dg
			dup.Data = append([]byte(nil), dg.Data...)
			out = append(out, dup)
		}
	}
	return out
}

// applyReorder swaps adjacent datagrams probabilistically within a bounded
// look-ahead window; sequence numbers stay inside the reorder window.
func applyReorder(in []Datagram, r *rng, rate float64, window uint16) []Datagram {
	if rate <= 0 || len(in) < 2 {
		return in
	}
	maxSwap := int(window) / 2
	if maxSwap < 1 {
		maxSwap = 1
	}
	out := append([]Datagram(nil), in...)
	for i := 0; i < len(out)-1; i++ {
		if r.float64() >= rate {
			continue
		}
		jump := 1 + int(r.float64()*float64(maxSwap))
		j := i + jump
		if j >= len(out) {
			j = len(out) - 1
		}
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// encodeSR builds a minimal RTCP Sender Report matching the internal parser.
// Wall time is expressed as NTP seconds/fraction relative to 1900.
func encodeSR(ssrc uint32, wallNS int64, rtpTS uint32) []byte {
	const headerLen, senderInfoLen = 8, 20
	lengthWords := (headerLen+senderInfoLen)/4 - 1
	b := make([]byte, (lengthWords+1)*4)
	b[0] = 0x80
	b[1] = 200 // RTCP SR
	b[2] = byte(lengthWords >> 8)
	b[3] = byte(lengthWords)
	putBE32(b[4:8], ssrc)

	unixSec := wallNS / 1_000_000_000
	ntpSec := uint32(unixSec) + ntpEpochOffsetU32
	ntpFrac := uint32(((wallNS % 1_000_000_000) << 32) / 1_000_000_000)
	putBE32(b[8:12], ntpSec)
	putBE32(b[12:16], ntpFrac)
	putBE32(b[16:20], rtpTS)
	return b
}

const ntpEpochOffsetU32 uint32 = 2208988800

func putBE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

// ParseDurationNS parses a duration string such as "30s" into nanoseconds,
// rejecting values outside the shared clock bound.
func ParseDurationNS(s string) (int64, *clock.Error) {
	if len(s) < 2 || s[len(s)-1] != 's' {
		return 0, clock.NewError(clock.ErrInvalidLength, "duration must end in 's', e.g. 30s")
	}
	num := int64(0)
	for i := 0; i < len(s)-1; i++ {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, clock.NewError(clock.ErrInvalidLength, "duration must be whole seconds")
		}
		num, _ = clock.Mul64(num, 10)
		num, _ = clock.Add64(num, int64(ch-'0'))
	}
	ns, err := clock.Mul64(num, 1_000_000_000)
	if err != nil {
		return 0, err
	}
	if ns <= 0 || ns > clock.MaxDurationMicros*1000 {
		return 0, clock.NewError(clock.ErrInvalidLength, "duration out of bounds")
	}
	return ns, nil
}
