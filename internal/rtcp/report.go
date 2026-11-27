// Package rtcp parses the RTCP report blocks used by the investigation: sender
// reports, receiver reports and the report blocks they carry.
//
// Only the fields needed for loss and jitter statistics are interpreted; other
// packets in a compound packet are skipped by their declared length. A field
// that ends before its declared size produces a structured error carrying the
// original byte offset and the RTCP stage, never a partially filled report.
package rtcp

import (
	"encoding/binary"
	"fmt"

	"rtp-rebuild/internal/fault"
)

// Packet types from RFC 3550.
const (
	TypeSR    = 200
	TypeRR    = 201
	TypeSDES  = 202
	TypeBye   = 203
	TypeApp   = 204
	TypeXR    = 207
	headerLen = 4
	blockLen  = 24
)

// ReportBlock is one reception report block.
type ReportBlock struct {
	SSRC           uint32
	FractionLost   uint8
	CumulativeLost int32 // 24 bit signed field, sign-extended
	HighestSeq     uint32
	Jitter         uint32
	LastSR         uint32
	DelaySinceSR   uint32
	// Offset is the absolute input offset of the block's first byte.
	Offset int64
}

// Report is a parsed SR or RR.
type Report struct {
	Type        uint8
	SSRC        uint32 // sender SSRC
	ReportCount int
	NTP         uint64 // SR only
	RTPTS       uint32 // SR only
	PacketCount uint32 // SR only
	OctetCount  uint32 // SR only
	Blocks      []ReportBlock
	// Offset is the absolute input offset of the RTCP packet header.
	Offset int64
}

// Summary is the aggregate across all parsed reports.
type Summary struct {
	Reports     []Report
	Blocks      int
	LostTotal   int64
	MaxJitter   uint32
	JitterSum   uint64
	FractionMax uint8
	// HighestSeq is the largest extended highest sequence seen in any block.
	HighestSeq uint32
}

// Jitter returns the mean interarrival jitter across report blocks.
func (s *Summary) Jitter() float64 {
	if s.Blocks == 0 {
		return 0
	}
	return float64(s.JitterSum) / float64(s.Blocks)
}

// ParseCompound parses one RTCP compound packet starting at data[0].
//
// base is the absolute input offset of data[0] and is used verbatim in errors
// and block offsets. Packets of unknown type are skipped, not failed, as long
// as their declared length is consistent.
func ParseCompound(data []byte, base int64) ([]Report, error) {
	var reports []Report
	off := 0
	for off < len(data) {
		if len(data)-off < headerLen {
			return reports, fault.At(fault.StageRTCP, "parse header", base+int64(off),
				fmt.Errorf("%w: %d trailing bytes", fault.ErrTruncated, len(data)-off))
		}
		hdr := data[off]
		if version := hdr >> 6; version != 2 {
			return reports, fault.At(fault.StageRTCP, "parse header", base+int64(off),
				fmt.Errorf("%w: rtcp version %d", fault.ErrCorrupt, version))
		}
		pt := data[off+1]
		lengthField := binary.BigEndian.Uint16(data[off+2 : off+4])
		total := (int(lengthField) + 1) * 4
		if total > len(data)-off {
			return reports, fault.At(fault.StageRTCP, "parse packet", base+int64(off),
				fmt.Errorf("%w: %s declares %d bytes, %d present", fault.ErrTruncated, typeName(pt), total, len(data)-off))
		}
		body := data[off+headerLen : off+total]
		switch pt {
		case TypeSR, TypeRR:
			rep, err := parseReport(pt, body, base+int64(off), int(hdr&0x1f))
			if err != nil {
				return reports, err
			}
			reports = append(reports, rep)
		case TypeSDES, TypeBye, TypeApp, TypeXR:
			// Not needed for loss/jitter; skipped by declared length.
		default:
			return reports, fault.At(fault.StageRTCP, "parse packet", base+int64(off),
				fmt.Errorf("%w: packet type %d", fault.ErrUnsupported, pt))
		}
		off += total
	}
	return reports, nil
}

func parseReport(pt byte, body []byte, absOff int64, count int) (Report, error) {
	rep := Report{Type: pt, ReportCount: count, Offset: absOff}
	minLen := 4 // sender SSRC
	if pt == TypeSR {
		minLen = 24 // 4 SSRC + 8 NTP + 4 RTPTS + 4 count + 4 octets
	}
	if len(body) < minLen {
		return rep, fault.At(fault.StageRTCP, "parse report", absOff,
			fmt.Errorf("%w: %s body %d bytes", fault.ErrTruncated, typeName(pt), len(body)))
	}
	rep.SSRC = binary.BigEndian.Uint32(body[0:4])
	pos := 4
	if pt == TypeSR {
		rep.NTP = binary.BigEndian.Uint64(body[4:12])
		rep.RTPTS = binary.BigEndian.Uint32(body[12:16])
		rep.PacketCount = binary.BigEndian.Uint32(body[16:20])
		rep.OctetCount = binary.BigEndian.Uint32(body[20:24])
		pos = 24
	}
	for i := 0; i < count; i++ {
		if pos+blockLen > len(body) {
			return rep, fault.At(fault.StageRTCP, "parse report block", absOff+int64(headerLen+pos),
				fmt.Errorf("%w: block %d of %d needs %d bytes, %d left",
					fault.ErrTruncated, i+1, count, blockLen, len(body)-pos))
		}
		blk := body[pos : pos+blockLen]
		raw := binary.BigEndian.Uint32(blk[4:8])
		// The cumulative loss field is signed: values above 0x7FFFFF are
		// negative (packets received after being declared lost).
		lost := int32(raw&0x00ffffff) << 8 >> 8
		rep.Blocks = append(rep.Blocks, ReportBlock{
			SSRC:           binary.BigEndian.Uint32(blk[0:4]),
			FractionLost:   blk[4],
			CumulativeLost: lost,
			HighestSeq:     binary.BigEndian.Uint32(blk[8:12]),
			Jitter:         binary.BigEndian.Uint32(blk[12:16]),
			LastSR:         binary.BigEndian.Uint32(blk[16:20]),
			DelaySinceSR:   binary.BigEndian.Uint32(blk[20:24]),
			Offset:         absOff + int64(headerLen+pos),
		})
		pos += blockLen
	}
	return rep, nil
}

// Summarize folds reports into aggregate statistics.
func Summarize(reports []Report) *Summary {
	s := &Summary{Reports: reports}
	for _, rep := range reports {
		for _, blk := range rep.Blocks {
			s.Blocks++
			s.LostTotal += int64(blk.CumulativeLost)
			s.JitterSum += uint64(blk.Jitter)
			if blk.Jitter > s.MaxJitter {
				s.MaxJitter = blk.Jitter
			}
			if blk.FractionLost > s.FractionMax {
				s.FractionMax = blk.FractionLost
			}
			if blk.HighestSeq > s.HighestSeq {
				s.HighestSeq = blk.HighestSeq
			}
		}
	}
	return s
}

func typeName(pt byte) string {
	switch pt {
	case TypeSR:
		return "sender report"
	case TypeRR:
		return "receiver report"
	case TypeSDES:
		return "sdes"
	case TypeBye:
		return "bye"
	case TypeApp:
		return "app"
	case TypeXR:
		return "extended report"
	default:
		return fmt.Sprintf("packet type %d", pt)
	}
}

// SenderReport encodes an SR with the given blocks.
func SenderReport(ssrc, rtpTS uint32, ntp uint64, pktCount, octetCount uint32, blocks []ReportBlock) []byte {
	n := 7 + 6*len(blocks) // 32 bit words
	buf := make([]byte, n*4)
	buf[0] = 0x80 | byte(len(blocks))
	buf[1] = TypeSR
	binary.BigEndian.PutUint16(buf[2:4], uint16(n-1))
	binary.BigEndian.PutUint32(buf[4:8], ssrc)
	binary.BigEndian.PutUint64(buf[8:16], ntp)
	binary.BigEndian.PutUint32(buf[16:20], rtpTS)
	binary.BigEndian.PutUint32(buf[20:24], pktCount)
	binary.BigEndian.PutUint32(buf[24:28], octetCount)
	writeBlocks(buf[28:], blocks)
	return buf
}

// ReceiverReport encodes an RR with the given blocks.
func ReceiverReport(ssrc uint32, blocks []ReportBlock) []byte {
	n := 2 + 6*len(blocks) // 32 bit words
	buf := make([]byte, n*4)
	buf[0] = 0x80 | byte(len(blocks))
	buf[1] = TypeRR
	binary.BigEndian.PutUint16(buf[2:4], uint16(n-1))
	binary.BigEndian.PutUint32(buf[4:8], ssrc)
	writeBlocks(buf[8:], blocks)
	return buf
}

func writeBlocks(dst []byte, blocks []ReportBlock) {
	for i, b := range blocks {
		blk := dst[i*blockLen : (i+1)*blockLen]
		binary.BigEndian.PutUint32(blk[0:4], b.SSRC)
		blk[4] = b.FractionLost
		binary.BigEndian.PutUint32(blk[8:12], b.HighestSeq)
		binary.BigEndian.PutUint32(blk[12:16], b.Jitter)
		binary.BigEndian.PutUint32(blk[16:20], b.LastSR)
		binary.BigEndian.PutUint32(blk[20:24], b.DelaySinceSR)
		putUint24(blk[5:8], uint32(b.CumulativeLost)&0x00ffffff)
	}
}

func putUint24(dst []byte, v uint32) {
	dst[0] = byte(v >> 16)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v)
}
