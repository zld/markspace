// Package stream validates RTP packet sequences delivered as a framed binary
// stream. The on-disk format is deterministic: each record is a 4-byte
// big-endian length, a 1-byte record kind and then that many payload bytes.
// Kind 0 is an RTP packet; other kinds are reserved for recovery metadata.
package stream

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"os"

	"github.com/pion/rtp"

	"mediasync/internal/fault"
)

// Record kinds inside the framed stream.
const (
	KindRTP byte = 0
	KindXOR byte = 1 // single-packet XOR redundancy frame
)

// Stream-level safety limits, mirroring the container package's bounds.
const (
	MaxPackets   = 1_000_000
	MaxFrameSize = 64 * 1024
)

// FrameHeaderSize is len(uint32) + len(kind byte).
const FrameHeaderSize = 5

// Packet is one parsed RTP packet plus its stream attribution.
type Packet struct {
	Sequence uint16
	RawTS    uint32
	Payload  []byte
	// Offset is the byte position of the frame header in the input file.
	Offset int64
	// Duplicate is set when another packet with the same sequence exists.
	Duplicate bool
	// Recovered marks packets synthesized by loss recovery.
	Recovered bool
}

// Record is one framed entry, before RTP-specific interpretation.
type Record struct {
	Kind    byte
	Payload []byte
	Offset  int64
}

// ReadAll parses the whole framed stream, enforcing frame and stream limits
// and returning every fault with the offending frame's original offset.
func ReadAll(ctx context.Context, path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var records []Record
	var offset int64
	var header [FrameHeaderSize]byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, fault.New(fault.ErrCanceled, "stream", offset, "canceled while reading")
		}
		n, err := io.ReadFull(r, header[:])
		if err == io.EOF {
			return records, nil
		}
		if err == io.ErrUnexpectedEOF {
			return nil, fault.New(fault.ErrTruncated, "stream", offset,
				"frame header needs %d bytes, got %d", FrameHeaderSize, n)
		}
		if err != nil {
			return nil, err
		}
		length := binary.BigEndian.Uint32(header[0:4])
		kind := header[4]
		if length == 0 || length > MaxFrameSize {
			return nil, fault.New(fault.ErrLimitExceeded, "stream", offset,
				"frame length %d outside (0,%d]", length, MaxFrameSize)
		}
		buf := make([]byte, length)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fault.New(fault.ErrTruncated, "stream", offset,
				"declared frame of %d bytes is truncated: %v", length, err)
		}
		records = append(records, Record{Kind: kind, Payload: buf, Offset: offset})
		offset += FrameHeaderSize + int64(length)
		if len(records) > MaxPackets {
			return nil, fault.New(fault.ErrLimitExceeded, "stream", offset,
				"packet count exceeds limit %d", MaxPackets)
		}
	}
}

// ParseRTP converts kind-0 records into packets via pion/rtp, rejecting any
// malformed packet with the original frame offset attached.
func ParseRTP(records []Record) ([]Packet, []Record, error) {
	var packets []Packet
	var xorFrames []Record
	for _, rec := range records {
		switch rec.Kind {
		case KindXOR:
			xorFrames = append(xorFrames, rec)
			continue
		case KindRTP:
		default:
			return nil, nil, fault.New(fault.ErrPacketMalformed, "stream", rec.Offset,
				"unknown frame kind %d", rec.Kind)
		}
		var p rtp.Packet
		if err := p.Unmarshal(rec.Payload); err != nil {
			return nil, nil, fault.New(fault.ErrPacketMalformed, "stream", rec.Offset,
				"rtp unmarshal: %v", err)
		}
		packets = append(packets, Packet{
			Sequence: p.SequenceNumber,
			RawTS:    p.Timestamp,
			Payload:  append([]byte(nil), p.Payload...),
			Offset:   rec.Offset,
		})
	}
	return packets, xorFrames, nil
}
