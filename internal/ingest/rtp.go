package ingest

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/pion/rtp"

	"tsindex/internal/fault"
)

// maxDatagramBytes bounds one length-prefixed local datagram. The format is
// strictly local (never read from a network socket); 64 KiB matches the
// two-byte length prefix and the usual RTP-over-UDP MTU envelope.
const maxDatagramBytes = 65535

// RTPTransport reads length-prefixed RTP datagrams from a local file and
// exposes their MPEG-TS payload as a plain byte stream for StreamReader.
// Each datagram on disk is: uint16 big-endian length followed by one
// RFC 3550 RTP packet whose payload is whole 188-byte TS packets.
type RTPTransport struct {
	src     io.Reader
	rec     *fault.Recorder
	pending []byte // TS payload bytes not yet consumed
	off     int64  // logical TS byte offset reported in faults
	ended   bool
}

// NewRTPTransport wraps a local datagram stream.
func NewRTPTransport(src io.Reader, rec *fault.Recorder) *RTPTransport {
	return &RTPTransport{src: src, rec: rec}
}

// Read implements io.Reader over the concatenated TS payloads.
func (t *RTPTransport) Read(p []byte) (int, error) {
	for len(t.pending) == 0 {
		if t.ended {
			return 0, io.EOF
		}
		if err := t.nextDatagram(); err != nil {
			return 0, err
		}
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	t.off += int64(n)
	return n, nil
}

func (t *RTPTransport) nextDatagram() error {
	var lenBuf [2]byte
	if _, err := io.ReadFull(t.src, lenBuf[:]); err != nil {
		if errors.Is(err, io.EOF) {
			t.ended = true
			return io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			t.rec.Record(fault.Fault{Kind: fault.KindTruncated, Offset: t.off,
				Recoverable: true, Detail: "truncated RTP length prefix"})
			t.ended = true
			return io.EOF
		}
		return err
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	if n == 0 {
		return errors.New("ingest: zero-length RTP datagram")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(t.src, buf); err != nil {
		t.rec.Record(fault.Fault{Kind: fault.KindTruncated, Offset: t.off,
			Recoverable: true, Detail: "truncated RTP datagram body"})
		t.ended = true
		return io.EOF
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(buf); err != nil {
		// A bad datagram is reported at its logical offset; the stream can
		// continue with the next length-prefixed record.
		t.rec.Record(fault.Fault{Kind: fault.KindCorruptPacket, Offset: t.off,
			Recoverable: true, Detail: "invalid RTP packet: " + err.Error()})
		return nil
	}
	if len(pkt.Payload) == 0 || len(pkt.Payload)%PacketSize != 0 {
		t.rec.Record(fault.Fault{Kind: fault.KindCorruptPacket, Offset: t.off,
			Recoverable: true, Detail: "RTP payload is not whole 188-byte TS packets"})
		return nil
	}
	t.pending = pkt.Payload
	return nil
}
