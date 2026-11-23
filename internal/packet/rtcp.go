package packet

import (
	"encoding/binary"

	"github.com/pion/rtcp"

	"mediaforge/internal/mediaerr"
)

// ReportBlock is one reception report block from an SR or RR.
type ReportBlock struct {
	SSRC             uint32
	FractionLost     uint8
	TotalLost        uint32
	HighestSeq       uint32
	Jitter           uint32
	LastSenderReport uint32
	Delay            uint32
}

// SenderReport is an RTCP SR with its optional reception blocks.
type SenderReport struct {
	SSRC        uint32
	NTPTime     uint64
	RTPTime     uint32
	PacketCount uint32
	OctetCount  uint32
	Reports     []ReportBlock
}

// ReceiverReport is an RTCP RR with its reception blocks.
type ReceiverReport struct {
	SSRC    uint32
	Reports []ReportBlock
}

// Reports is the recovered RTCP state from one datagram. Errors holds
// per-subpacket failures; successfully parsed packets are still returned.
type Reports struct {
	Senders   []SenderReport
	Receivers []ReceiverReport
	Errors    []error
}

// ParseReports decodes every RTCP subpacket in a compound datagram. A malformed
// outer framing is fatal; malformed subpackets are recorded and skipped.
func ParseReports(datagram []byte, offset int64) (*Reports, error) {
	if len(datagram) < 4 {
		return nil, mediaerr.At(mediaerr.CodeTruncated, offset, 0, false,
			"RTCP datagram needs 4 bytes, got %d", len(datagram))
	}
	if datagram[0]>>6 != 2 {
		return nil, mediaerr.At(mediaerr.CodeBadPacket, offset, 0, false,
			"unsupported RTCP version %d", datagram[0]>>6)
	}
	out := &Reports{}
	for cursor := 0; cursor < len(datagram); {
		if len(datagram)-cursor < 4 {
			out.Errors = append(out.Errors, mediaerr.At(mediaerr.CodeTruncated,
				offset+int64(cursor), 0, false, "trailing RTCP bytes"))
			break
		}
		length := (int(binary.BigEndian.Uint16(datagram[cursor+2:cursor+4])) + 1) * 4
		if length > len(datagram)-cursor {
			out.Errors = append(out.Errors, mediaerr.At(mediaerr.CodeTruncated,
				offset+int64(cursor), 0, false,
				"RTCP packet declares %d bytes, only %d remain", length, len(datagram)-cursor))
			break
		}
		sub := datagram[cursor : cursor+length]
		cursor += length
		if err := parseSubpacket(out, sub); err != nil {
			out.Errors = append(out.Errors, err)
		}
	}
	return out, nil
}

func parseSubpacket(out *Reports, sub []byte) error {
	var h rtcp.Header
	if err := h.Unmarshal(sub); err != nil {
		return mediaerr.New(mediaerr.CodeBadPacket, "RTCP header: %v", err)
	}
	if h.Padding {
		isPaddingLengthValid := len(sub) > 0 && int(sub[len(sub)-1]) <= len(sub)-4 && sub[len(sub)-1] != 0
		if !isPaddingLengthValid {
			return mediaerr.New(mediaerr.CodeBadPacket, "invalid RTCP padding")
		}
		sub = sub[:len(sub)-int(sub[len(sub)-1])]
	}
	switch h.Type {
	case rtcp.TypeSenderReport:
		var sr rtcp.SenderReport
		if err := sr.Unmarshal(sub); err != nil {
			return mediaerr.New(mediaerr.CodeBadPacket, "RTCP SR: %v", err)
		}
		out.Senders = append(out.Senders, SenderReport{
			SSRC:        sr.SSRC,
			NTPTime:     sr.NTPTime,
			RTPTime:     sr.RTPTime,
			PacketCount: sr.PacketCount,
			OctetCount:  sr.OctetCount,
			Reports:     convertReports(sr.Reports),
		})
	case rtcp.TypeReceiverReport:
		var rr rtcp.ReceiverReport
		if err := rr.Unmarshal(sub); err != nil {
			return mediaerr.New(mediaerr.CodeBadPacket, "RTCP RR: %v", err)
		}
		out.Receivers = append(out.Receivers, ReceiverReport{
			SSRC:    rr.SSRC,
			Reports: convertReports(rr.Reports),
		})
	}
	return nil
}

func convertReports(in []rtcp.ReceptionReport) []ReportBlock {
	if len(in) == 0 {
		return nil
	}
	out := make([]ReportBlock, len(in))
	for i, r := range in {
		out[i] = ReportBlock{
			SSRC:             r.SSRC,
			FractionLost:     r.FractionLost,
			TotalLost:        r.TotalLost,
			HighestSeq:       r.LastSequenceNumber,
			Jitter:           r.Jitter,
			LastSenderReport: r.LastSenderReport,
			Delay:            r.Delay,
		}
	}
	return out
}

// DeclaredLoss sums the cumulative loss reported by all RR blocks.
func (r *Reports) DeclaredLoss() uint64 {
	var total uint64
	for _, rr := range r.Receivers {
		for _, b := range rr.Reports {
			total += uint64(b.TotalLost)
		}
	}
	for _, sr := range r.Senders {
		for _, b := range sr.Reports {
			total += uint64(b.TotalLost)
		}
	}
	return total
}

// HighestSequence returns the highest extended sequence number reported.
func (r *Reports) HighestSequence() (uint32, bool) {
	var best uint32
	found := false
	for _, rr := range r.Receivers {
		for _, b := range rr.Reports {
			if !found || b.HighestSeq > best {
				best, found = b.HighestSeq, true
			}
		}
	}
	return best, found
}
