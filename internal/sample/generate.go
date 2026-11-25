package sample

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	pionrtp "github.com/pion/rtp"

	"mediacheck/internal/report"
	rtpcheck "mediacheck/internal/rtp"
)

const (
	CaseNormal           = "normal"
	CaseTruncatedCluster = "truncated-cluster"
	CaseAudioConflict    = "audio-conflict"
	CaseRTPGap           = "rtp-gap"
)

// Fixture SSRCs identify the two normal-demo tracks.
const (
	SSRCVideo = 0x56494401 // "VID\x01"
	SSRCAudio = 0x41554402 // "AUD\x02"
)

// Bundle is a fully self-generated local input. No external fixture is read.
type Bundle struct {
	Case   string
	Seed   int64
	EBML   []byte
	RTP    [][]byte
	Digest string
}

// Generate creates a named deterministic sample. Seed changes filler bytes but
// not element lengths or offsets, keeping failure offsets stable.
func Generate(caseName string, seed int64) (*Bundle, error) {
	if caseName == "" {
		caseName = CaseNormal
	}
	var (
		data []byte
		raw  [][]byte
		err  error
	)
	switch caseName {
	case CaseNormal:
		data, raw, err = buildNormal(seed, false, false)
	case CaseRTPGap:
		data, raw, err = buildNormal(seed, false, true)
	case CaseTruncatedCluster:
		data, raw, err = buildNormal(seed, true, false)
	case CaseAudioConflict:
		data, raw, err = buildAudioConflict(seed)
	default:
		return nil, fmt.Errorf("unknown sample case %q", caseName)
	}
	if err != nil {
		return nil, err
	}
	b := &Bundle{Case: caseName, Seed: seed, EBML: data, RTP: raw}
	b.Digest = DigestBundle(b)
	return b, nil
}

// ValidCase reports whether name is an accepted --case value.
func ValidCase(name string) bool {
	switch name {
	case "", CaseNormal, CaseTruncatedCluster, CaseAudioConflict, CaseRTPGap:
		return true
	default:
		return false
	}
}

// DigestBundle hashes length-prefixed EBML and serialized RTP packets.
func DigestBundle(b *Bundle) string {
	h := sha256.New()
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(b.EBML)))
	h.Write(length[:])
	h.Write(b.EBML)
	for _, pkt := range b.RTP {
		binary.BigEndian.PutUint64(length[:], uint64(len(pkt)))
		h.Write(length[:])
		h.Write(pkt)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Streams unmarshals generated RTP packets and assigns fixture clock rates.
func (b *Bundle) Streams() ([]rtpcheck.Stream, *report.Error) {
	bySSRC := map[uint32]*rtpcheck.Stream{}
	var order []uint32
	for i, raw := range b.RTP {
		p, perr := rtpcheck.ParsePacket(raw, i)
		if perr != nil {
			return nil, perr
		}
		s, ok := bySSRC[p.SSRC]
		if !ok {
			track, rate := trackForSSRC(p.SSRC)
			s = &rtpcheck.Stream{Track: track, SSRC: p.SSRC, ClockRate: rate, FragmentIndex: 0}
			bySSRC[p.SSRC] = s
			order = append(order, p.SSRC)
		}
		s.Packets = append(s.Packets, p)
	}
	out := make([]rtpcheck.Stream, 0, len(order))
	for _, ssrc := range order {
		out = append(out, *bySSRC[ssrc])
	}
	return out, nil
}

func trackForSSRC(ssrc uint32) (int, uint32) {
	switch ssrc {
	case SSRCVideo:
		return 1, 90_000
	case SSRCAudio:
		return 2, 48_000
	default:
		return 0, 90_000
	}
}

func marshalRTP(seq uint16, ts uint32, ssrc uint32, payloadType uint8, marker bool, payload []byte) ([]byte, error) {
	pkt := pionrtp.Packet{
		Header: pionrtp.Header{
			Version:        2,
			Marker:         marker,
			PayloadType:    payloadType,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: payload,
	}
	return pkt.Marshal()
}
