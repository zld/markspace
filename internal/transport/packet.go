// Package transport parses RTP packets carrying the project metadata subset
// and repairs duplicates, reordering and burst loss.
package transport

import (
	"encoding/binary"

	"github.com/pion/rtp"

	"avcheck/internal/media"
)

// ExtID is the RTP header-extension id carrying PacketMeta. Payloads longer
// than 16 bytes make pion/rtp emit the two-byte-header extension form.
const ExtID uint8 = 4

// extLen: streamID(4) frameSeq(8) ticks(8) key(1) metaVersion(1) length(4) sum(32).
const extLen = 4 + 8 + 8 + 1 + 1 + 4 + 32

// PacketMeta is the project-defined RTP metadata subset, one frame per packet.
type PacketMeta struct {
	StreamID    uint32
	FrameSeq    uint64
	Ticks       int64
	Key         bool
	MetaVersion uint8
	Length      uint32
	Sum         [32]byte
}

// Frame converts the packet into a media frame without validating it.
func (m PacketMeta) Frame(payload []byte) *media.Frame {
	return &media.Frame{
		StreamID: m.StreamID,
		Seq:      m.FrameSeq,
		Ticks:    m.Ticks,
		Key:      m.Key,
		Meta:     media.Meta{Version: m.MetaVersion, Length: m.Length},
		Sum:      m.Sum,
		Payload:  payload,
	}
}

func (m PacketMeta) marshal() []byte {
	b := make([]byte, extLen)
	binary.BigEndian.PutUint32(b[0:], m.StreamID)
	binary.BigEndian.PutUint64(b[4:], m.FrameSeq)
	binary.BigEndian.PutUint64(b[12:], uint64(m.Ticks))
	if m.Key {
		b[20] = 1
	}
	b[21] = m.MetaVersion
	binary.BigEndian.PutUint32(b[22:], m.Length)
	copy(b[26:], m.Sum[:])
	return b
}

func unmarshalMeta(b []byte) (PacketMeta, error) {
	var m PacketMeta
	if len(b) < extLen {
		return m, media.NewError(media.KindTruncated, 0, 0, int64(len(b)),
			"extension metadata %d bytes, want %d", len(b), extLen)
	}
	m.StreamID = binary.BigEndian.Uint32(b[0:])
	m.FrameSeq = binary.BigEndian.Uint64(b[4:])
	m.Ticks = int64(binary.BigEndian.Uint64(b[12:]))
	m.Key = b[20] != 0
	m.MetaVersion = b[21]
	m.Length = binary.BigEndian.Uint32(b[22:])
	copy(m.Sum[:], b[26:])
	return m, nil
}

// EncodePacket builds a marshaled RTP packet for one frame.
func EncodePacket(m PacketMeta, payload []byte, rtpSeq uint16, ts uint32, ssrc uint32) ([]byte, error) {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         m.Key,
			PayloadType:    96,
			SequenceNumber: rtpSeq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: payload,
	}
	if err := p.SetExtension(ExtID, m.marshal()); err != nil {
		return nil, media.NewError(media.KindCorrupt, m.StreamID, m.FrameSeq, -1, "set extension: %v", err)
	}
	raw, err := p.Marshal()
	if err != nil {
		return nil, media.NewError(media.KindCorrupt, m.StreamID, m.FrameSeq, -1, "marshal: %v", err)
	}
	return raw, nil
}

// DecodePacket parses a raw RTP packet and its metadata extension.
func DecodePacket(raw []byte) (*rtp.Packet, PacketMeta, error) {
	var p rtp.Packet
	var m PacketMeta
	if err := p.Unmarshal(raw); err != nil {
		return nil, m, media.NewError(media.KindCorrupt, 0, 0, 0, "rtp unmarshal: %v", err)
	}
	ext := p.GetExtension(ExtID)
	if ext == nil {
		return nil, m, media.NewError(media.KindCorrupt, 0, 0, 0, "missing metadata extension id=%d", ExtID)
	}
	meta, err := unmarshalMeta(ext)
	if err != nil {
		return nil, m, err
	}
	return &p, meta, nil
}
