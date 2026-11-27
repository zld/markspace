package stream

import (
	"encoding/binary"
	"os"

	"github.com/pion/rtp"

	perr "gamepack/internal/errors"
)

// RTPRecord is one on-disk RTP packet record: a little-endian fixed header
// followed by the raw RTP packet bytes. The fixture generator writes this
// layout; the scanner reads it without any network dependency.
type RTPRecord struct {
	RTP       rtp.Packet
	RawLength uint16
}

// ReadRTPSource loads a sequence of RTP packets from a flat local file.
// Each record is: uint16 little-endian packet length, then packet bytes.
// Sequence numbers must be contiguous after de-jitter; a gap is a boundary
// error anchored at the record position.
func ReadRTPSource(path string) ([]*rtp.Packet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, perr.New(subsystem, perr.CodeInvalidEncoding,
			"cannot read RTP source %q: %v", path, err)
	}
	var out []*rtp.Packet
	at := int64(0)
	for at < int64(len(raw)) {
		if at+2 > int64(len(raw)) {
			return nil, perr.At(subsystem, perr.CodeTruncatedUnit, at,
				"RTP record length header truncated")
		}
		n := int64(binary.LittleEndian.Uint16(raw[at : at+2]))
		at += 2
		if n <= 0 || at+n > int64(len(raw)) {
			return nil, perr.At(subsystem, perr.CodeTruncatedUnit, at,
				"RTP record declares %d bytes but file ends at %d", n, len(raw))
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(raw[at : at+n]); err != nil {
			return nil, perr.At(subsystem, perr.CodeInvalidEncoding, at,
				"malformed RTP packet: %v", err).Wrap(err)
		}
		out = append(out, pkt)
		at += n
	}
	if len(out) == 0 {
		return nil, perr.New(subsystem, perr.CodeIndexMissing,
			"RTP source %q contains no packets", path)
	}
	return out, nil
}

// AttachTransportCoordinates pairs each keyframe boundary with the RTP
// timestamp and sequence number of the packet that carries its unit. The
// RTP source must contain one packet per scanned unit, in unit order.
func AttachTransportCoordinates(res *ScanResult, packets []*rtp.Packet) error {
	if len(packets) != len(res.Units) {
		return perr.New(subsystem, perr.CodeIndexMissing,
			"RTP source has %d packets but stream has %d units",
			len(packets), len(res.Units))
	}
	byOrdinal := make(map[int]*rtp.Packet, len(packets))
	for i, p := range packets {
		byOrdinal[i] = p
	}
	for i := range res.Boundaries {
		b := &res.Boundaries[i]
		p, ok := byOrdinal[b.Ordinal]
		if !ok {
			return perr.At(subsystem, perr.CodeIndexMissing, b.Offset,
				"no RTP packet for boundary ordinal %d", b.Ordinal)
		}
		b.RTPTimestamp = p.Timestamp
		b.RTPSeq = p.SequenceNumber
	}
	return nil
}

// ValidateBoundaries enforces that boundaries are ordered, non-overlapping
// and cover the stream from the first keyframe onward. It returns the first
// offending boundary's original byte offset.
func ValidateBoundaries(res *ScanResult) error {
	if len(res.Boundaries) == 0 {
		return perr.New(subsystem, perr.CodeIndexMissing, "no boundaries to validate")
	}
	prev := int64(-1)
	for _, b := range res.Boundaries {
		if b.Offset <= prev {
			return perr.At(subsystem, perr.CodeBoundaryConflict, b.Offset,
				"boundary at byte %d is not strictly ordered", b.Offset)
		}
		if b.Length <= 0 || b.Offset+b.Length > res.Units[len(res.Units)-1].End {
			return perr.At(subsystem, perr.CodeOffsetOutOfRange, b.Offset,
				"boundary at byte %d length %d exceeds stream extent", b.Offset, b.Length)
		}
		if !b.Key {
			return perr.At(subsystem, perr.CodeIndexMissing, b.Offset,
				"boundary at byte %d is not marked keyframe", b.Offset)
		}
		prev = b.Offset
	}
	return nil
}
