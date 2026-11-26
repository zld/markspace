package publish

import (
	"encoding/json"
	"os"

	"github.com/pion/rtp"

	"regress/internal/fault"
	"regress/internal/timebase"
)

// AuditPacket is one RTP time-audit record for a media segment. The packet is
// produced with pion/rtp and its timestamp is derived from the shared integer
// time base, so manifest timing and audit timing must agree exactly.
type AuditPacket struct {
	Sequence     uint16 `json:"sequence"`
	Timestamp    uint32 `json:"rtp_timestamp_90khz"`
	SegmentSeq   uint32 `json:"segment_sequence"`
	BaseTimeTicks uint64 `json:"base_time_ticks"`
}

// AuditTrail is the full set of audit packets plus SSRC metadata.
type AuditTrail struct {
	SSRC        uint32        `json:"ssrc"`
	ClockRate   uint32        `json:"clock_rate"`
	PayloadType uint8         `json:"payload_type"`
	Packets     []AuditPacket `json:"packets"`
}

// BuildAuditTrail generates one RTP packet per segment using pion/rtp.
// Timestamps convert each segment's base decode time (in the track
// timescale) to the canonical 90 kHz clock through the shared time base.
// Packets are constructed directly (rather than via the streaming
// packetizer) so each segment carries its absolute base timestamp; the
// packetizer's accumulating timestamp model is unsuitable for per-fragment
// absolute timing.
func BuildAuditTrail(metas []*SegmentMeta, ssrc uint32, payloadType uint8) (*AuditTrail, error) {
	trail := &AuditTrail{SSRC: ssrc, ClockRate: uint32(timebase.Rate), PayloadType: payloadType}
	for i, meta := range metas {
		r, err := timebase.New(meta.BaseTime, uint64(meta.Timescale))
		if err != nil {
			return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
				"audit time base invalid").Wrap(err)
		}
		ticks, err := r.Ticks()
		if err != nil {
			return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
				"audit timestamp overflow").Wrap(err)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    payloadType,
				SSRC:           ssrc,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(ticks), // RTP timestamps are uint32 by spec
			},
			Payload: []byte{0x01},
		}
		// Round-trip through the wire format to prove the packet is a valid
		// pion/rtp packet and the timestamp survives serialization.
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
				"rtp marshal failed").Wrap(err)
		}
		var back rtp.Packet
		if err := back.Unmarshal(raw); err != nil {
			return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
				"rtp unmarshal failed").Wrap(err)
		}
		if back.Timestamp != pkt.Timestamp || back.SequenceNumber != pkt.SequenceNumber {
			return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, "",
				"rtp round trip altered timing fields")
		}
		trail.Packets = append(trail.Packets, AuditPacket{
			Sequence:      pkt.SequenceNumber,
			Timestamp:     pkt.Timestamp,
			SegmentSeq:    meta.Sequence,
			BaseTimeTicks: meta.BaseTime,
		})
	}
	return trail, nil
}

// VerifyAuditTrail re-derives every timestamp and confirms it matches the
// packetized record. The verifier calls this rather than trusting the file.
func VerifyAuditTrail(trail *AuditTrail, metas []*SegmentMeta) error {
	if len(trail.Packets) != len(metas) {
		return fault.New(fault.StageVerify, fault.CodeManifestInconsistent, "",
			"audit packet count does not match segment count")
	}
	expectedSeq := uint16(0)
	for i, pkt := range trail.Packets {
		if pkt.Sequence != expectedSeq {
			return fault.New(fault.StageVerify, fault.CodeManifestInconsistent, "",
				"audit RTP sequence gap")
		}
		expectedSeq++
		r, err := timebase.New(metas[i].BaseTime, uint64(metas[i].Timescale))
		if err != nil {
			return err
		}
		ticks, err := r.Ticks()
		if err != nil {
			return err
		}
		if pkt.Timestamp != uint32(ticks) || pkt.SegmentSeq != metas[i].Sequence {
			return fault.New(fault.StageVerify, fault.CodeManifestInconsistent, "",
				"audit timestamp does not match segment base time")
		}
	}
	return nil
}

// WriteAuditTrail serializes the audit trail.
func WriteAuditTrail(t *AuditTrail, path string) error {
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

// ReadAuditTrail parses a previously written audit trail.
func ReadAuditTrail(path string) (*AuditTrail, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t AuditTrail
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fault.New(fault.StageVerify, fault.CodeManifestInconsistent, "",
			"audit trail parse failed").Wrap(err)
	}
	return &t, nil
}
