// Package rtplog defines the on-disk record container used by the generated
// fixtures and by ingest. A file starts with an 8-byte magic header followed
// by length-prefixed records:
//
//	uint32 LE payloadLen (0 < payloadLen <= media.MaxPacketBytes)
//	uint64 LE arrivalMicros (receiver arrival, microseconds since epoch)
//	byte[payloadLen] RTP packet (github.com/pion/rtp wire format)
//
// All lengths are bounds-checked against the remaining bytes and the global
// media.MaxInputBytes cap, so a corrupt prefix cannot trigger a huge alloc.
package rtplog

import (
	"bufio"
	"encoding/binary"
	"io"
	"time"

	"rtpreplay/media"
)

// Magic is the fixed file header.
var Magic = [8]byte{'R', 'T', 'P', 'L', 'O', 'G', '1', 0}

// Record is one parsed rtplog entry.
type Record struct {
	Index   int           // zero-based record position in the file
	Offset  int64         // byte offset of the record's length prefix
	Arrival time.Time     // receiver arrival time
	Packet  []byte        // RTP packet bytes (owned by the record)
}

// HeaderSize is the magic header length.
const HeaderSize = 8

var le = binary.LittleEndian

// WriteHeader emits the file magic.
func WriteHeader(w io.Writer) error {
	_, err := w.Write(Magic[:])
	return err
}

// WriteRecord appends one record. It rejects empty and oversized payloads.
func WriteRecord(w io.Writer, arrival time.Time, packet []byte) error {
	if len(packet) == 0 {
		return media.NewError(media.KindBadInput, "EMPTY_RECORD", "rtp", -1, "zero-length RTP packet")
	}
	if len(packet) > media.MaxPacketBytes {
		return media.NewError(media.KindBadInput, "RECORD_TOO_LARGE", "rtp", -1,
			"packet %d bytes exceeds %d", len(packet), media.MaxPacketBytes)
	}
	var hdr [12]byte
	le.PutUint32(hdr[:4], uint32(len(packet)))
	le.PutUint64(hdr[4:12], uint64(arrival.UnixMicro()))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(packet)
	return err
}

// Reader sequentially scans an rtplog stream with bounded reads.
type Reader struct {
	r        *bufio.Reader
	index    int
	consumed int64 // bytes consumed including the header
}

// NewReader validates the magic header and returns a bounded record reader.
func NewReader(r io.Reader, totalSize int64) (*Reader, error) {
	br := bufio.NewReader(r)
	var hdr [8]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return nil, media.NewError(media.KindBadInput, "MISSING_HEADER", "rtp", 0,
			"cannot read magic: %v", err)
	}
	if hdr != Magic {
		return nil, media.NewError(media.KindBadInput, "BAD_MAGIC", "rtp", 0, "not an RTPLOG1 file")
	}
	if totalSize > media.MaxInputBytes {
		return nil, media.NewError(media.KindBadInput, "INPUT_TOO_LARGE", "rtp", -1,
			"input %d bytes exceeds cap %d", totalSize, media.MaxInputBytes)
	}
	return &Reader{r: br, consumed: HeaderSize}, nil
}

// Next returns the next record or io.EOF at clean end of file.
func (rd *Reader) Next() (*Record, error) {
	off := rd.consumed
	var hdr [12]byte
	n, err := io.ReadFull(rd.r, hdr[:])
	rd.consumed += int64(n)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if n == 0 && err == io.EOF {
			return nil, io.EOF
		}
		return nil, media.NewError(media.KindBadInput, "TRUNCATED_RECORD_HEADER", "rtp", off,
			"%d of 12 header bytes remain", n)
	}
	if err != nil {
		return nil, err
	}
	plen := int(le.Uint32(hdr[:4]))
	if plen == 0 {
		return nil, media.NewError(media.KindBadInput, "EMPTY_RECORD", "rtp", off, "zero payload length")
	}
	if plen > media.MaxPacketBytes {
		return nil, media.NewError(media.KindBadInput, "LENGTH_OUT_OF_BOUNDS", "rtp", off,
			"declared payload %d exceeds packet cap %d", plen, media.MaxPacketBytes)
	}
	pkt := make([]byte, plen)
	pn, perr := io.ReadFull(rd.r, pkt)
	rd.consumed += int64(pn)
	if perr != nil {
		return nil, media.NewError(media.KindBadInput, "TRUNCATED_RECORD_PAYLOAD", "rtp", off,
			"declared %d payload bytes but only %d remain", plen, pn)
	}
	rec := &Record{
		Index:   rd.index,
		Offset:  off,
		Arrival: time.UnixMicro(int64(le.Uint64(hdr[4:12]))).UTC(),
		Packet:  pkt,
	}
	rd.index++
	return rec, nil
}

// Consumed reports the number of bytes read so far.
func (rd *Reader) Consumed() int64 { return rd.consumed }
