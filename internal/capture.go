package medpipe

import (
	"bytes"
	"encoding/binary"

	"github.com/pion/rtp"
)

// CaptureMagic prefixes every medpipe RTP capture file. The on-disk layout is
// a sequence of records: big-endian uint16 length followed by one RTP packet
// of exactly that many bytes. Length framing (rather than Annex B start
// codes) guarantees a truncated trailing packet is detected at a precise
// byte offset instead of being mistaken for a complete small unit.
var CaptureMagic = [4]byte{'M', 'P', 'R', '1'}

// maxRecordSize bounds a single length-prefixed record; anything larger is a
// corrupt length field rather than a legitimate packet.
const maxRecordSize = 1 << 16

// CaptureBuilder assembles an MPR1 length-prefixed capture in memory. It is
// used both by the fixture generator (production code) and the unit tests, so
// the wire format is exercised through one implementation.
type CaptureBuilder struct {
	buf bytes.Buffer
}

// NewCaptureBuilder starts a capture with the mandatory MPR1 magic.
func NewCaptureBuilder() *CaptureBuilder {
	cb := &CaptureBuilder{}
	cb.buf.Write(CaptureMagic[:])
	return cb
}

// Packet appends one RTP record: big-endian uint16 length then packet bytes.
func (cb *CaptureBuilder) Packet(seq uint16, ts uint32, marker bool, payload []byte) *CaptureBuilder {
	pkt := rtp.Packet{
		Header:  rtp.Header{SequenceNumber: seq, Timestamp: ts, Marker: marker, Version: 2, PayloadType: 96, SSRC: 1},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(raw)))
	cb.buf.Write(l[:])
	cb.buf.Write(raw)
	return cb
}

// Bytes returns the serialized capture.
func (cb *CaptureBuilder) Bytes() []byte { return cb.buf.Bytes() }

// Truncate drops the last n bytes to simulate a cut-off trailing record.
func (cb *CaptureBuilder) Truncate(n int) *CaptureBuilder {
	b := cb.buf.Bytes()
	cb.buf.Reset()
	cb.buf.Write(b[:len(b)-n])
	return cb
}
