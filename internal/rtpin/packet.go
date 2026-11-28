// Package rtpin parses segmented RTP transport records. Each record in a
// trace file carries the arrival time, the file offset the payload belongs
// to, and one RTP packet whose payload is a chunk of the source file.
//
// Record layout (big-endian):
//
//	[8B arrival ns][8B file offset][2B RTP packet length][RTP packet]
package rtpin

import (
	"encoding/binary"
	"io"

	"github.com/pion/rtp"

	"mediakernel/internal/fault"
)

// RecordHeaderLen is the fixed trace record header size in bytes.
const RecordHeaderLen = 18

// Record is one parsed transport event.
type Record struct {
	TraceOffset int64  // byte offset of this record inside the trace file
	ArrivalNs   int64  // arrival timestamp, nanoseconds (monotonic, synthetic)
	FileOffset  uint64 // offset of the payload inside the source file
	Sequence    uint16 // RTP sequence number
	Timestamp   uint32 // RTP timestamp
	Payload     []byte // file chunk carried by this packet
}

// ReadRecord reads exactly one record from r. recOffset is the byte offset
// of the record start inside the trace, used for error reporting. io.EOF is
// returned only when no bytes of a new record are available; a partial
// record yields ERR_TRUNCATED_PACKET.
func ReadRecord(r io.Reader, recOffset int64) (Record, error) {
	var hdr [RecordHeaderLen]byte
	n, err := io.ReadFull(r, hdr[:])
	if err != nil {
		if err == io.EOF && n == 0 {
			return Record{}, io.EOF
		}
		return Record{}, fault.New(fault.ErrTruncatedPacket, recOffset,
			"record header truncated: got %d of %d bytes", n, RecordHeaderLen)
	}
	rec := Record{
		TraceOffset: recOffset,
		ArrivalNs:   int64(binary.BigEndian.Uint64(hdr[0:8])),
		FileOffset:  binary.BigEndian.Uint64(hdr[8:16]),
	}
	pktLen := int(binary.BigEndian.Uint16(hdr[16:18]))
	buf := make([]byte, pktLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return Record{}, fault.New(fault.ErrTruncatedPacket, recOffset,
			"packet body truncated: want %d bytes", pktLen)
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(buf); err != nil {
		return Record{}, fault.New(fault.ErrTruncatedPacket, recOffset,
			"RTP header parse failed: %v", err)
	}
	rec.Sequence = pkt.SequenceNumber
	rec.Timestamp = pkt.Timestamp
	rec.Payload = pkt.Payload
	return rec, nil
}

// WriteRecord appends one record to w. Used by the sample generator.
func WriteRecord(w io.Writer, arrivalNs int64, fileOffset uint64, pkt *rtp.Packet) error {
	raw, err := pkt.Marshal()
	if err != nil {
		return err
	}
	if len(raw) > 0xFFFF {
		return fault.New(fault.ErrInvalidArg, fault.NA, "RTP packet too large: %d", len(raw))
	}
	var hdr [RecordHeaderLen]byte
	binary.BigEndian.PutUint64(hdr[0:8], uint64(arrivalNs))
	binary.BigEndian.PutUint64(hdr[8:16], fileOffset)
	binary.BigEndian.PutUint16(hdr[16:18], uint16(len(raw)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}
