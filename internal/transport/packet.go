// Package transport parses RTP packet bytes, orders them per stream, and
// records loss spans. It deliberately does no media decoding.
package transport

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"mediadist/internal/merr"
)

// Limits bounds every packet parse and cache so a hostile or corrupt input
// cannot force an unbounded allocation.
type Limits struct {
	MaxPacketBytes int
	MaxPackets     int
	MaxDropped     int
}

// DefaultLimits are sized for local demo fixtures: 64 KiB packets and 4096
// buffered packets per stream.
func DefaultLimits() Limits {
	return Limits{MaxPacketBytes: 64 * 1024, MaxPackets: 4096, MaxDropped: 1024}
}

// Validate normalizes zero fields to defaults and rejects negative values.
func (l Limits) Validate() (Limits, error) {
	if l.MaxPacketBytes < 0 || l.MaxPackets < 0 || l.MaxDropped < 0 {
		return l, merr.New(merr.StageTransport, "bad_limits", merr.NoSpan(0), false,
			"negative limit")
	}
	if l.MaxPacketBytes == 0 {
		l.MaxPacketBytes = DefaultLimits().MaxPacketBytes
	}
	if l.MaxPackets == 0 {
		l.MaxPackets = DefaultLimits().MaxPackets
	}
	if l.MaxDropped == 0 {
		l.MaxDropped = DefaultLimits().MaxDropped
	}
	if l.MaxPacketBytes > 16*1024*1024 {
		return l, merr.New(merr.StageTransport, "bad_limits", merr.NoSpan(0), false,
			"MaxPacketBytes %d exceeds hard cap", l.MaxPacketBytes)
	}
	return l, nil
}

// Packet is a parsed RTP packet plus the fields the pipeline needs. Raw holds
// a copy of the original datagram so hashes are computed over received bytes.
type Packet struct {
	Stream    int
	Seq       uint16
	Timestamp uint32
	SSRC      uint32
	PayloadType uint8
	Marker    bool
	CSRC      []uint32
	Extensions []rtp.Extension
	ExtensionProfile uint16
	PaddingSize int
	Payload   []byte
	Raw       []byte
	RecvIndex int
}

// ParseOptions controls parsing leniency.
type ParseOptions struct {
	Stream int
	// AllowPaddingBecauseHeaderOnly accepts a packet whose padding claims more
	// bytes than remain, recording the inconsistency as a recoverable error.
	Strict bool
}

// Parse parses one datagram fully: fixed header, CSRC list, extension block,
// payload and padding. On error it returns the sequence number it could read
// (so the caller can log it) together with a structured merr.Error.
func Parse(datagram []byte, opts ParseOptions) (*Packet, error) {
	p := &Packet{Stream: opts.Stream}
	if len(datagram) < 12 {
		return p, merr.New(merr.StageTransport, "short_header",
			merr.OffsetSpan(opts.Stream, 0, int64(len(datagram))), false,
			"datagram has %d bytes, RTP fixed header needs 12", len(datagram))
	}
	// Sequence number lives at a fixed offset and is readable even when later
	// fields turn out to be malformed, so record it first.
	p.Seq = binary.BigEndian.Uint16(datagram[2:4])
	p.Timestamp = binary.BigEndian.Uint32(datagram[4:8])

	var rp rtp.Packet
	if err := rp.Unmarshal(datagram); err != nil {
		return p, merr.Wrap(merr.StageTransport, "malformed_rtp",
			merr.SeqSpan(opts.Stream, p.Seq), false, err,
			"RTP unmarshal failed at offset %d", parseFailureOffset(datagram))
	}
	if rp.Version != 2 {
		return p, merr.New(merr.StageTransport, "bad_version",
			merr.SeqSpan(opts.Stream, p.Seq), false,
			"RTP version %d, expected 2", rp.Version)
	}
	p.Seq = rp.SequenceNumber
	p.Timestamp = rp.Timestamp
	p.SSRC = rp.SSRC
	p.PayloadType = rp.PayloadType
	p.Marker = rp.Marker
	p.CSRC = append([]uint32(nil), rp.CSRC...)
	p.Extensions = append([]rtp.Extension(nil), rp.Extensions...)
	p.ExtensionProfile = rp.ExtensionProfile
	p.PaddingSize = int(rp.PaddingSize)
	p.Payload = append([]byte(nil), rp.Payload...)
	p.Raw = append([]byte(nil), datagram...)

	if opts.Strict {
		if len(datagram) > 0 && rp.Padding && int(rp.PaddingSize) > len(datagram)-12 {
			return p, merr.New(merr.StageTransport, "bad_padding",
				merr.OffsetSpan(opts.Stream, int64(len(datagram)-1), 1), true,
				"padding %d exceeds payload region", rp.PaddingSize)
		}
		if rp.Extension && rp.ExtensionProfile != 0xBEDE && rp.ExtensionProfile != 0x1000 {
			return p, merr.New(merr.StageTransport, "unknown_extension",
				merr.SeqSpan(opts.Stream, p.Seq), true,
				"extension profile 0x%04x is not one- or two-byte", rp.ExtensionProfile)
		}
	}
	return p, nil
}

// parseFailureOffset best-effort locates the byte where parsing stopped.
func parseFailureOffset(datagram []byte) int64 {
	if len(datagram) < 12 {
		return int64(len(datagram))
	}
	n := 12 + 4*int(datagram[0]&0x0F)
	if n > len(datagram) {
		return int64(len(datagram))
	}
	if datagram[0]&0x10 != 0 && n+4 <= len(datagram) {
		n += 4 + 4*int(binary.BigEndian.Uint16(datagram[n+2:n+4]))
		if n > len(datagram) {
			return int64(len(datagram))
		}
	}
	return int64(n)
}

// ParseResult groups accepted packets and recoverable parse errors.
type ParseResult struct {
	Packets []*Packet
	Errors  []*merr.Error
	// Skipped counts datagrams rejected but skipped.
	Skipped int
}

// ParseStream parses a slice of datagrams for one stream, recording malformed
// packets as recoverable span errors and continuing with the rest.
func ParseStream(stream int, datagrams [][]byte, limit Limits, strict bool) (*ParseResult, error) {
	limit, err := limit.Validate()
	if err != nil {
		return nil, err
	}
	if len(datagrams) > limit.MaxPackets {
		return nil, merr.New(merr.StageTransport, "packet_limit", merr.NoSpan(stream), false,
			"stream %d supplied %d packets, limit %d", stream, len(datagrams), limit.MaxPackets)
	}
	res := &ParseResult{Packets: make([]*Packet, 0, len(datagrams))}
	for i, dg := range datagrams {
		if len(dg) > limit.MaxPacketBytes {
			res.Errors = append(res.Errors, merr.New(merr.StageTransport, "packet_too_large",
				merr.OffsetSpan(stream, 0, int64(len(dg))), true,
				"datagram %d is %d bytes, limit %d", i, len(dg), limit.MaxPacketBytes))
			res.Skipped++
			continue
		}
		p, perr := Parse(dg, ParseOptions{Stream: stream, Strict: strict})
		if perr != nil {
			if e := merr.AsError(perr); e != nil {
				res.Errors = append(res.Errors, e)
			}
			res.Skipped++
			continue
		}
		p.RecvIndex = i
		res.Packets = append(res.Packets, p)
	}
	return res, nil
}

// ByteRange returns the raw byte footprint of the packet for evidence hashing.
func (p *Packet) ByteRange() merr.Span {
	return merr.OffsetSpan(p.Stream, 0, int64(len(p.Raw)))
}

// String is a compact deterministic description for reports.
func (p *Packet) String() string {
	return fmt.Sprintf("s%d#%d ts=%d len=%d marker=%t", p.Stream, p.Seq, p.Timestamp, len(p.Raw), p.Marker)
}
