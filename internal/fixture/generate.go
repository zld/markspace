// Package fixture generates deterministic synthetic RTP/RTCP streams plus
// the ground-truth description that tests and the demo CLI assert against.
//
// No external media files are used. A media unit is encoded in a single RTP
// packet whose payload starts with a fixed 12-byte header (see MediaHeader).
// The generator can inject packet loss, duplication, reordering, payload
// corruption and 16-bit sequence-number wrap while still reporting exactly
// which units a correct pipeline should recover.
package fixture

import (
	"encoding/binary"
	"math/rand"

	"github.com/pion/rtp"
)

// Payload magic lets the analyzer distinguish our synthetic codec from
// arbitrary bytes and detect truncated/corrupt payloads reliably.
const (
	Magic0        byte = 0x4D // 'M'
	Magic1        byte = 0x43 // 'C'
	MediaHeaderLen     = 12
	// MediaFlagKey marks a random-access (key) media unit.
	MediaFlagKey byte = 0x01
	// MediaFlagCorrupt flips payload bytes after the header so the parser
	// must detect corruption via the payload checksum.
	MediaFlagCorrupt byte = 0x02
)

// TrackKind identifies the two synthetic tracks.
type TrackKind string

const (
	TrackVideo TrackKind = "video"
	TrackAudio TrackKind = "audio"
)

// TrackConfig describes one generated RTP track.
type TrackConfig struct {
	ID        uint32
	Kind      TrackKind
	SSRC      uint32
	ClockRate uint32
	// PT is the RTP payload type.
	PT uint8
	// Units is the number of media units (and RTP packets) generated.
	Units int
	// TickDelta is the RTP timestamp increment per media unit.
	TickDelta uint32
	// StartSeq is the first (16-bit) RTP sequence number; wrap is exercised
	// by choosing values close to 65535.
	StartSeq uint16
	// StartTS is the first RTP timestamp.
	StartTS uint32
	// KeyEvery marks every n-th video unit as a key frame (video only).
	KeyEvery int
}

// MediaHeader is the fixed payload prefix of every synthetic media packet.
type MediaHeader struct {
	Magic     [2]byte
	TrackID   uint16
	UnitIndex uint32
	Flags     uint8
	// BodyLen is the number of deterministic body bytes following the header.
	BodyLen uint16
	// Checksum is the XOR-folding checksum over header (zeroed field) and body.
	Checksum uint8
}

// Marshal encodes the 12-byte header.
func (h MediaHeader) Marshal() []byte {
	b := make([]byte, MediaHeaderLen)
	b[0], b[1] = h.Magic[0], h.Magic[1]
	binary.BigEndian.PutUint16(b[2:4], h.TrackID)
	binary.BigEndian.PutUint32(b[4:8], h.UnitIndex)
	b[8] = h.Flags
	binary.BigEndian.PutUint16(b[9:11], h.BodyLen)
	b[11] = h.Checksum
	return b
}

// ParseMediaHeader decodes the header and reports its first error offset
// (relative to payload start) when the bytes cannot be interpreted.
func ParseMediaHeader(b []byte) (MediaHeader, int64, error) {
	var h MediaHeader
	if len(b) < MediaHeaderLen {
		return h, int64(len(b)), errShortHeader
	}
	h.Magic = [2]byte{b[0], b[1]}
	h.TrackID = binary.BigEndian.Uint16(b[2:4])
	h.UnitIndex = binary.BigEndian.Uint32(b[4:8])
	h.Flags = b[8]
	h.BodyLen = binary.BigEndian.Uint16(b[9:11])
	h.Checksum = b[11]
	if h.Magic[0] != Magic0 || h.Magic[1] != Magic1 {
		return h, 0, errBadMagic
	}
	return h, -1, nil
}

// BuildPayload creates a deterministic, checksummed media-unit payload.
func BuildPayload(trackID uint32, unitIndex uint32, flags byte, bodyLen int) []byte {
	hdr := MediaHeader{
		Magic:     [2]byte{Magic0, Magic1},
		TrackID:   uint16(trackID),
		UnitIndex: unitIndex,
		Flags:     flags,
		BodyLen:   uint16(bodyLen),
	}
	body := deterministicBody(trackID, unitIndex, bodyLen)
	buf := make([]byte, 0, MediaHeaderLen+bodyLen)
	buf = append(buf, hdr.Marshal()...)
	buf = append(buf, body...)
	buf[11] = xorChecksum(buf[:11], body)
	return buf
}

// deterministicBody makes unit content reproducible from (track, unit) so
// the same seed always yields byte-identical streams and derived hashes.
func deterministicBody(trackID uint32, unitIndex uint32, bodyLen int) []byte {
	body := make([]byte, bodyLen)
	rng := rand.New(rand.NewSource(int64(trackID)<<32 | int64(unitIndex)))
	_, _ = rng.Read(body)
	return body
}

// xorChecksum folds all bytes (with the checksum slot skipped by callers)
// into one byte so a single flipped bit is always detectable.
func xorChecksum(head, body []byte) byte {
	var c byte
	for _, b := range head {
		c ^= b
	}
	for _, b := range body {
		c ^= b
	}
	return c
}

// VerifyChecksum validates a full payload produced by BuildPayload.
func VerifyChecksum(payload []byte) bool {
	if len(payload) < MediaHeaderLen {
		return false
	}
	got := payload[11]
	head := append([]byte(nil), payload[:MediaHeaderLen]...)
	head[11] = 0
	return got == xorChecksum(head, payload[MediaHeaderLen:])
}

// RawPacket is a generated RTP packet on the wire, before network damage.
type RawPacket struct {
	TrackID uint32
	Packet  *rtp.Packet
	// UnitIndex / IsKey / Tick are ground truth for the originating unit.
	UnitIndex uint32
	IsKey     bool
	Tick      uint32
}

// DefaultVideoConfig produces 120 video units at 30 fps (3000 ticks @90kHz),
// a key frame every 30 units, starting near the 16-bit wrap boundary.
func DefaultVideoConfig() TrackConfig {
	return TrackConfig{
		ID: 1, Kind: TrackVideo, SSRC: 0x11110001, ClockRate: 90000,
		PT: 96, Units: 120, TickDelta: 3000,
		StartSeq: 0, StartTS: 90000, KeyEvery: 30,
	}
}

// DefaultAudioConfig produces 600 audio units at 50 ms spacing (2400 ticks
// @48kHz), so audio and video cover the same ~4 s media span.
func DefaultAudioConfig() TrackConfig {
	return TrackConfig{
		ID: 2, Kind: TrackAudio, SSRC: 0x22220002, ClockRate: 48000,
		PT: 97, Units: 600, TickDelta: 2400,
		StartSeq: 0, StartTS: 48000, KeyEvery: 0,
	}
}

// generateTrack builds the ordered, undamaged packet list for one track.
func generateTrack(cfg TrackConfig) []*RawPacket {
	out := make([]*RawPacket, 0, cfg.Units)
	seq := cfg.StartSeq
	ts := cfg.StartTS
	for i := 0; i < cfg.Units; i++ {
		flags := byte(0)
		isKey := cfg.Kind == TrackVideo && cfg.KeyEvery > 0 && i%cfg.KeyEvery == 0
		if isKey {
			flags |= MediaFlagKey
		}
		bodyLen := 24
		if cfg.Kind == TrackAudio {
			bodyLen = 16
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    cfg.PT,
				SSRC:           cfg.SSRC,
				SequenceNumber: seq,
				Timestamp:      ts,
				Marker:         isKey,
			},
			Payload: BuildPayload(cfg.ID, uint32(i), flags, bodyLen),
		}
		out = append(out, &RawPacket{
			TrackID: cfg.ID, Packet: pkt, UnitIndex: uint32(i),
			IsKey: isKey, Tick: ts,
		})
		seq++
		ts += cfg.TickDelta
	}
	return out
}
