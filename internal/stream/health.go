package stream

import "mediapipe/internal/model"

// Health contains the required stream quality counters.
type Health struct {
	RTPPackets      int64         `json:"rtp_packets"`
	RTCPPackets     int64         `json:"rtcp_packets"`
	PacketLost      int64         `json:"packet_lost"`
	PacketDuplicate int64         `json:"packet_duplicate"`
	PacketLate      int64         `json:"packet_late"`
	PacketCorrupt   int64         `json:"packet_corrupt"`
	RTCPInvalid     int64         `json:"rtcp_invalid,omitempty"`
	Truncated       bool          `json:"truncated"`
	TruncateOffset  int64         `json:"truncate_offset,omitempty"`
	HeaderOffset    int64         `json:"header_offset,omitempty"`
	Issues          []model.Error `json:"issues,omitempty"`
}

func (h *Health) CountRecord(rec *RawRecord) {
	if rec == nil {
		return
	}
	if rec.Header.Type == RecordRTP && rec.RTPPacket != nil {
		h.RTPPackets++
	}
	if rec.Header.Type == RecordRTCP && len(rec.RTCPPackets) > 0 {
		h.RTCPPackets += int64(len(rec.RTCPPackets))
	}
	if rec.Corrupt {
		h.PacketCorrupt++
	}
	if rec.ParseError != nil && rec.Header.Type == RecordRTCP {
		h.RTCPInvalid++
	}
}

func (h *Health) AddIssue(err error) {
	if err == nil {
		return
	}
	if me, ok := err.(*model.Error); ok {
		h.Issues = append(h.Issues, *me)
		switch me.Class {
		case model.ErrorTruncated:
			h.Truncated = true
			h.TruncateOffset = me.Offset
		case model.ErrorHeader:
			h.HeaderOffset = me.Offset
		}
	}
}

func (h *Health) MarkTruncated(offset int64) {
	h.Truncated = true
	h.TruncateOffset = offset
}

// Snapshot returns a value copy safe for reports.
func (h *Health) Snapshot() Health {
	out := *h
	if h.Issues != nil {
		out.Issues = append([]model.Error(nil), h.Issues...)
	}
	return out
}
