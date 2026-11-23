package adapter

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/pion/rtp"

	"mediad/timeline"
)

// RTP packet stream fixture framing: "RTP1" magic, uint32 packet count, then
// for each packet a uint32 length followed by a serialized RTP packet.
const rtpMagic = "RTP1"

// RTPAdapter parses RTP packets into a video track. Sequence numbers are
// reordered, the 32-bit timestamp is unwrapped and truncation is detected.
type RTPAdapter struct{}

func (RTPAdapter) Name() string     { return "rtp" }
func (RTPAdapter) Version() Version { return Version{Major: 1, Minor: 0} }

func (RTPAdapter) Parse(r io.Reader, opts Options) (*ParseResult, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(raw) < 8 {
		return nil, fmt.Errorf("rtp: truncated header: %d bytes", len(raw))
	}
	if string(raw[:4]) != rtpMagic {
		return nil, fmt.Errorf("rtp: bad magic %q", raw[:4])
	}
	count := binary.BigEndian.Uint32(raw[4:8])
	pos := 8
	type pkt struct {
		p     rtp.Packet
		order int
	}
	var pkts []pkt
	for i := uint32(0); i < count; i++ {
		if pos+4 > len(raw) {
			return nil, fmt.Errorf("rtp: truncated packet %d length field", i)
		}
		pLen := int(binary.BigEndian.Uint32(raw[pos : pos+4]))
		pos += 4
		if pLen <= 0 || pos+pLen > len(raw) {
			return nil, fmt.Errorf("rtp: illegal length packet %d: len=%d remaining=%d", i, pLen, len(raw)-pos)
		}
		var p rtp.Packet
		if err := p.Unmarshal(raw[pos : pos+pLen]); err != nil {
			return nil, fmt.Errorf("rtp: packet %d: %w", i, err)
		}
		pos += pLen
		pkts = append(pkts, pkt{p: p, order: int(i)})
	}
	if pos != len(raw) {
		return nil, fmt.Errorf("rtp: %d trailing bytes", len(raw)-pos)
	}
	// Reorder by sequence number (out-of-order delivery is valid input).
	sort.SliceStable(pkts, func(a, b int) bool {
		return pkts[a].p.SequenceNumber < pkts[b].p.SequenceNumber
	})
	trackID := opts.TrackID
	if trackID == 0 {
		trackID = 2
	}
	wrap := opts.WrapModulus
	if wrap == 0 {
		wrap = 1 << 32
	}
	tr := timeline.Track{ID: trackID, Kind: timeline.KindVideo, Clock: timeline.Clock{
		Timescale:   opts.Timescale,
		OriginNS:    opts.OriginNS,
		WrapModulus: wrap,
		MaxDriftNS:  opts.MaxDriftNS,
	}}
	res := &ParseResult{}
	for i, item := range pkts {
		p := item.p
		if i > 0 && p.SequenceNumber != (pkts[i-1].p.SequenceNumber+1)&0xffff {
			return nil, fmt.Errorf("rtp: out-of-order/unrecoverable sequence gap before seq=%d", p.SequenceNumber)
		}
		payload := append([]byte(nil), p.Payload...)
		ev := timeline.Event{
			TrackID: trackID, Kind: timeline.KindVideo, Tick: int64(p.Timestamp),
			Seq: int64(item.order), Keyframe: p.Marker, Payload: payload, Size: len(payload),
		}
		if len(payload) >= 4 && binary.BigEndian.Uint32(payload[:4]) != sampleMagic {
			ev.Corrupt = true
		}
		tr.Events = append(tr.Events, ev)
	}
	res.Tracks = []timeline.Track{tr}
	return res, nil
}

// RTPWriter emits the RTP packet stream fixture framing.
type RTPWriter struct {
	buf bytes.Buffer
	n   uint32
}

func NewRTPWriter() *RTPWriter {
	w := &RTPWriter{}
	w.buf.WriteString(rtpMagic)
	w.buf.Write([]byte{0, 0, 0, 0})
	return w
}

// WritePacket appends one RTP packet in its serialized form.
func (w *RTPWriter) WritePacket(p *rtp.Packet) error {
	b, err := p.Marshal()
	if err != nil {
		return err
	}
	_ = binary.Write(&w.buf, binary.BigEndian, uint32(len(b)))
	w.buf.Write(b)
	w.n++
	return nil
}

// Bytes finalizes the packet count and returns the container.
func (w *RTPWriter) Bytes() []byte {
	out := w.buf.Bytes()
	binary.BigEndian.PutUint32(out[4:8], w.n)
	return append([]byte(nil), out...)
}

// NewRTPPacket builds a fixture packet with the standard sample magic.
func NewRTPPacket(ssrc uint32, seq uint16, ts uint32, marker bool, payloadSize int) *rtp.Packet {
	payload := make([]byte, payloadSize)
	binary.BigEndian.PutUint32(payload, sampleMagic)
	return &rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts,
		SSRC: ssrc, Marker: marker,
	}, Payload: payload}
}
