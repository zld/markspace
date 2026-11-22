package pipeline

import (
	"encoding/binary"
	"math/rand"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// Stream record framing: [1 byte type][2 byte big-endian length][payload].
const (
	recTypeRTP  = 0
	recTypeRTCP = 1
	recHdrLen   = 3
)

const (
	genSSRC           = 0x12345678
	genPackets        = 160
	genKeyframeEvery  = 30
	genClockRate      = 90000
	genTicksPerPacket = 3000 // 30 packets per second at 90 kHz
)

// Scenarios lists every generatable sample stream, in stable order.
var Scenarios = []string{
	"normal", "reorder", "burstloss", "duplicate",
	"wrap", "truncnal", "corruptlen", "noindex",
}

// IsScenario reports whether s names a single scenario.
func IsScenario(s string) bool {
	for _, v := range Scenarios {
		if v == s {
			return true
		}
	}
	return false
}

type record struct {
	isRTCP  bool
	payload []byte
}

// generate builds the deterministic sample stream for a scenario.
func generate(seed int64, scenario string) []byte {
	rng := rand.New(rand.NewSource(seed))
	seq0 := uint16(rng.Intn(1 << 16))
	ts0 := rng.Uint32()
	if scenario == "wrap" {
		ts0 = 0xFFFFFFFF - 25*genTicksPerPacket // wrap mid-stream
	}

	pkts := make([][]byte, 0, genPackets)
	for i := 0; i < genPackets; i++ {
		nalType := byte(1)
		switch i % genKeyframeEvery {
		case 0:
			nalType = 5 // IDR
		case 1:
			nalType = 7 // SPS
		case 2:
			nalType = 8 // PPS
		}
		nal := []byte{0, 0, 0, 1, 0x60 | nalType}
		nal = append(nal, rngBytes(rng, 20+rng.Intn(40))...)
		p := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: seq0 + uint16(i),
				Timestamp:      ts0 + uint32(i)*genTicksPerPacket,
				SSRC:           genSSRC,
				Marker:         i%genKeyframeEvery == 0,
			},
			Payload: nal,
		}
		raw, err := p.Marshal()
		if err != nil {
			panic(err)
		}
		pkts = append(pkts, raw)
	}

	switch scenario {
	case "reorder":
		swap(pkts, 20, 21)
		swap(pkts, 40, 41)
		// Hold packet 10 back past the watermark: a late arrival.
		late := pkts[10]
		copy(pkts[10:55], pkts[11:56])
		pkts[55] = late
	case "burstloss":
		pkts = append(pkts[:60], pkts[69:]...) // 9-packet burst
	case "duplicate":
		pkts = append(pkts[:32], append([][]byte{pkts[30], pkts[31]}, pkts[32:]...)...)
	case "corruptlen":
		// Packet 50: padding bit set with zero padding length.
		pkts[50][0] |= 0x20
		pkts[50][len(pkts[50])-1] = 0x00
		// Packet 70: payload reduced to a bare start code (empty NAL).
		pkts[70] = append(pkts[70][:12], 0, 0, 0, 1)
	}

	recs := make([]record, 0, len(pkts)+4)
	for i, raw := range pkts {
		recs = append(recs, record{payload: raw})
		if (i+1)%50 == 0 {
			sr := &rtcp.SenderReport{SSRC: genSSRC, NTPTime: uint64(i), RTPTime: uint32(i)}
			if b, err := sr.Marshal(); err == nil {
				recs = append(recs, record{isRTCP: true, payload: b})
			}
		}
	}

	var buf []byte
	for _, r := range recs {
		typ := byte(recTypeRTP)
		if r.isRTCP {
			typ = recTypeRTCP
		}
		var hdr [recHdrLen]byte
		hdr[0] = typ
		binary.BigEndian.PutUint16(hdr[1:], uint16(len(r.payload)))
		buf = append(buf, hdr[:]...)
		buf = append(buf, r.payload...)
	}
	if scenario == "truncnal" {
		last := recs[len(recs)-1].payload
		buf = buf[:len(buf)-len(last)/2] // cut the final NAL in half
	}
	return buf
}

func rngBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Intn(256))
	}
	return b
}

func swap(s [][]byte, i, j int) { s[i], s[j] = s[j], s[i] }
