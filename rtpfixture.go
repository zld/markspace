package mediacore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"
)

// RTPFaultConfig controls how a synthetic RTP stream is disturbed.
type RTPFaultConfig struct {
	Seed      int64
	Packets   int
	LossBurst int // consecutive packets dropped starting at a random point
	Reorder   int // packets whose order is swapped with a neighbour
	Duplicate int // packets sent twice
	Window    uint32
	TrackID   uint32
	ClockRate uint32
	Timestamp uint32 // first RTP timestamp
	TSDelta   uint32 // timestamp step between packets
}

// RTPFixture is a generated, disturbed RTP stream plus ground truth.
type RTPFixture struct {
	Config    RTPFaultConfig
	Wire      [][]byte
	Dropped   []uint16
	Sent      int
	Expected  []uint16
	Payload   map[uint16][]byte
	SourceSum string
}

// GenerateRTPFaults builds a deterministic RTP stream with duplicates,
// reordering and one burst of loss. Payload bytes are derived from the seed
// and never modified by the fault injector.
func GenerateRTPFaults(cfg RTPFaultConfig) (*RTPFixture, error) {
	if cfg.Packets <= 0 {
		return nil, fail(KindInvalidConfig, "rtp-fixture", "packet count must be positive")
	}
	if cfg.ClockRate == 0 {
		cfg.ClockRate = 90000
	}
	if cfg.TrackID == 0 {
		cfg.TrackID = 1
	}
	if cfg.TSDelta == 0 {
		cfg.TSDelta = 3000
	}
	rnd := rand.New(rand.NewSource(cfg.Seed))
	f := &RTPFixture{Config: cfg, Payload: map[uint16][]byte{}}
	const ssrc = 0x1234abcd
	const pt = 96
	for i := 0; i < cfg.Packets; i++ {
		seq := uint16(i)
		payload := make([]byte, 24+rnd.Intn(16))
		for j := range payload {
			payload[j] = byte((i*37 + j*11 + int(cfg.Seed)) & 0xff)
		}
		f.Payload[seq] = payload
		f.Wire = append(f.Wire, marshalRTP(ssrc, pt, seq, cfg.Timestamp+uint32(i)*cfg.TSDelta, payload, i == cfg.Packets-1))
	}
	h := sha256.New()
	for i := 0; i < cfg.Packets; i++ {
		h.Write(f.Payload[uint16(i)])
	}
	f.SourceSum = hex.EncodeToString(h.Sum(nil))

	// One contiguous loss burst, placed away from both ends. The start is
	// bounded so the entire requested burst fits; callers can therefore assert
	// missing_count == LossBurst exactly.
	start := 1
	if maxStart := cfg.Packets - cfg.LossBurst - 1; maxStart >= 1 {
		start = 1 + rnd.Intn(maxStart) // start in [1, maxStart]
	}
	for i := 0; i < cfg.LossBurst && start+i < cfg.Packets; i++ {
		seq := uint16(start + i)
		f.Dropped = append(f.Dropped, seq)
	}
	// Remove dropped packets from the wire.
	kept := f.Wire[:0]
	for i, pkt := range f.Wire {
		if containsSeq(cfg.Packets, f.Dropped, uint16(i)) {
			continue
		}
		kept = append(kept, pkt)
	}
	f.Wire = append([][]byte(nil), kept...)

	// Reorder: swap requested pairs, then duplicate selected packets. Both
	// operate on the surviving set so a duplicate can reorder too.
	if cfg.Reorder > 0 && len(f.Wire) > 2 {
		for i := 0; i < cfg.Reorder; i++ {
			a := rnd.Intn(len(f.Wire) - 1)
			f.Wire[a], f.Wire[a+1] = f.Wire[a+1], f.Wire[a]
		}
	}
	if cfg.Duplicate > 0 && len(f.Wire) > 1 {
		var dupes [][]byte
		for i := 0; i < cfg.Duplicate; i++ {
			idx := rnd.Intn(len(f.Wire))
			dupes = append(dupes, f.Wire[idx])
		}
		f.Wire = append(f.Wire, dupes...)
	}
	f.Sent = len(f.Wire)
	for i := 0; i < cfg.Packets; i++ {
		if !containsSeq(cfg.Packets, f.Dropped, uint16(i)) {
			f.Expected = append(f.Expected, uint16(i))
		}
	}
	return f, nil
}

func containsSeq(n int, set []uint16, v uint16) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func marshalRTP(ssrc uint32, pt uint8, seq uint16, ts uint32, payload []byte, marker bool) []byte {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    pt,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
			Marker:         marker,
		},
		Payload: payload,
	}
	b, err := p.Marshal()
	if err != nil {
		panic(fmt.Sprintf("rtp marshal: %v", err)) // impossible for valid inputs
	}
	return b
}
