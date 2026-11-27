package synthetic

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/rand"

	"github.com/pion/rtp"
)

// ErrTruncatedCapture means a length-prefixed RTP record is incomplete.
var ErrTruncatedCapture = errors.New("truncated RTP capture record")

// RTPCapture is a deterministic RTP packet stream plus its ground truth.
type RTPCapture struct {
	SSRC         uint32
	ClockRate    uint64
	Packets      []rtp.Packet
	// FirstTS/FirstSeq are the raw values the stream starts at.
	FirstTS      uint32
	FirstSeq     uint16
}

// BuildRTPCapture generates count audio packets at 48kHz with 160-sample
// frames (20ms), optionally forcing wrap-around near both moduli. Packets are
// returned in transmission order, which can be shuffled via indices.
func BuildRTPCapture(rng *rand.Rand, ssrc uint32, count int, wrap bool) RTPCapture {
	const clockRate = 48000
	const frameSamples = 160
	firstSeq := uint16(0)
	firstTS := uint32(0)
	if wrap {
		firstSeq = uint16(65536 - 4)
		// Start four frame durations before the 32-bit modulus boundary.
		firstTS = math.MaxUint32 - uint32(4*frameSamples) + 1
	}
	cap := RTPCapture{SSRC: ssrc, ClockRate: clockRate, FirstSeq: firstSeq, FirstTS: firstTS}
	for i := 0; i < count; i++ {
		p := rtp.Packet{}
		p.Version = 2
		p.PayloadType = 96
		p.SSRC = ssrc
		p.SequenceNumber = uint16(uint32(firstSeq) + uint32(i))
		p.Timestamp = firstTS + uint32(i*frameSamples)
		p.Marker = i == count-1
		payload := make([]byte, frameSamples)
		for k := range payload {
			payload[k] = byte(rng.Intn(64)) + 64
		}
		p.Payload = payload
		cap.Packets = append(cap.Packets, p)
	}
	return cap
}

// Reorder swaps the packets at two positions (used to create in-window jitter).
func (c *RTPCapture) Reorder(i, j int) {
	c.Packets[i], c.Packets[j] = c.Packets[j], c.Packets[i]
}

// Duplicate appends a deep copy of an existing packet (idempotency testing).
func (c *RTPCapture) Duplicate(i int) {
	p := c.Packets[i]
	cp := rtp.Packet{}
	cp.Version = p.Version
	cp.PayloadType = p.PayloadType
	cp.SSRC = p.SSRC
	cp.Marker = p.Marker
	cp.SequenceNumber = p.SequenceNumber
	cp.Timestamp = p.Timestamp
	cp.Payload = append([]byte(nil), p.Payload...)
	c.Packets = append(c.Packets, cp)
}

// Encode writes the capture as length-prefixed binary RTP packets:
// each record is uint32 big-endian length followed by the marshaled packet.
func (c *RTPCapture) Encode() ([]byte, error) {
	var buf bytes.Buffer
	for i := range c.Packets {
		raw, err := c.Packets[i].Marshal()
		if err != nil {
			return nil, err
		}
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(len(raw)))
		buf.Write(hdr[:])
		buf.Write(raw)
	}
	return buf.Bytes(), nil
}

// DecodeRTP reads the length-prefixed format back into packets.
func DecodeRTP(data []byte) ([]*rtp.Packet, error) {
	var out []*rtp.Packet
	for off := 0; off < len(data); {
		if off+4 > len(data) {
			return nil, ErrTruncatedCapture
		}
		n := int(binary.BigEndian.Uint32(data[off:]))
		off += 4
		if n <= 0 || off+n > len(data) {
			return nil, ErrTruncatedCapture
		}
		p := &rtp.Packet{}
		if err := p.Unmarshal(data[off : off+n]); err != nil {
			return nil, err
		}
		out = append(out, p)
		off += n
	}
	return out, nil
}
