package ingest

import (
	"fmt"

	"github.com/pion/rtp"

	"mediacore/internal/fault"
)

// RTPHeaderSize is the fixed RTP v2 header length without CSRCs or extensions.
const RTPHeaderSize = 12

// RTPObservation is the result of reading one sample's RTP header.
type RTPObservation struct {
	TrackID   uint32 `json:"track_id"`
	Sample    int    `json:"sample_index"`
	Sequence  uint16 `json:"sequence"`
	SSRC      uint32 `json:"ssrc"`
	Timestamp uint32 `json:"timestamp"`
	Marker    bool   `json:"marker"`
}

// RTPReport summarizes RTP header and sequence continuity for a track.
type RTPReport struct {
	Packets      []RTPObservation `json:"packets"`
	SequenceGaps int              `json:"sequence_gaps"`
	Duplicates   int              `json:"duplicates"`
	Reordered    int              `json:"reordered"`
	Malformed    int              `json:"malformed"`
	Errors       []*fault.Error   `json:"-"`
}

// AnalyzeRTP reads an RTP header from the start of every sample payload and
// checks sequence continuity per SSRC. Sequence arithmetic is modulo 2^16, so
// wraparound is treated as normal progress, not as a gap.
//
// Samples whose payload is not a valid RTP v2 header are reported as
// malformed at the sample byte offset; they are not silently ignored.
func AnalyzeRTP(fragments []Fragment, data map[string][]byte) *RTPReport {
	rep := &RTPReport{}
	last := make(map[uint32]uint16)
	seen := make(map[uint32]uint64)
	for _, frag := range fragments {
		buf, ok := data[frag.File]
		if !ok {
			continue
		}
		for _, s := range frag.Samples {
			end := s.Offset + s.Size
			if s.Offset < 0 || end > int64(len(buf)) || s.Size < RTPHeaderSize {
				rep.Malformed++
				rep.Errors = append(rep.Errors, fault.New(fault.CodeBadPayload, frag.File, s.Offset, "trun",
					fmt.Sprintf("sample %d is too short for an RTP header", s.Index)).WithFragment(frag.Sequence))
				continue
			}
			var h rtp.Header
			if n, err := h.Unmarshal(buf[s.Offset:end]); err != nil || n < RTPHeaderSize || h.Version != 2 {
				rep.Malformed++
				rep.Errors = append(rep.Errors, fault.New(fault.CodeBadPayload, frag.File, s.Offset, "trun",
					fmt.Sprintf("sample %d is not a valid RTP packet: %v", s.Index, err)).WithFragment(frag.Sequence))
				continue
			}
			obs := RTPObservation{
				TrackID:   frag.TrackID,
				Sample:    s.Index,
				Sequence:  h.SequenceNumber,
				SSRC:      h.SSRC,
				Timestamp: h.Timestamp,
				Marker:    h.Marker,
			}
			rep.Packets = append(rep.Packets, obs)
			prev, ok := last[h.SSRC]
			if !ok {
				last[h.SSRC] = h.SequenceNumber
				seen[h.SSRC]++
				continue
			}
			delta := int16(h.SequenceNumber - prev)
			switch {
			case delta == 0 || seen[h.SSRC] > 0 && h.SequenceNumber == prev:
				rep.Duplicates++
				rep.Errors = append(rep.Errors, fault.New(fault.CodeDuplicateFragment, frag.File, s.Offset, "rtp",
					fmt.Sprintf("rtp sequence %d repeated for ssrc %d", h.SequenceNumber, h.SSRC)).WithFragment(frag.Sequence))
			case delta < 0:
				rep.Reordered++
				rep.Errors = append(rep.Errors, fault.New(fault.CodeBadPayload, frag.File, s.Offset, "rtp",
					fmt.Sprintf("rtp sequence went from %d to %d for ssrc %d", prev, h.SequenceNumber, h.SSRC)).WithFragment(frag.Sequence))
			case delta > 1:
				rep.SequenceGaps++
				rep.Errors = append(rep.Errors, fault.New(fault.CodeSequenceGap, frag.File, s.Offset, "rtp",
					fmt.Sprintf("rtp sequence jumped from %d to %d (%d missing) for ssrc %d", prev, h.SequenceNumber, delta-1, h.SSRC)).WithFragment(frag.Sequence))
			}
			last[h.SSRC] = h.SequenceNumber
			seen[h.SSRC]++
		}
	}
	return rep
}

// WriteRTPHeader stamps an RTP v2 header into the first bytes of payload.
// It is used by the fixture to model RTP-carried samples.
func WriteRTPHeader(payload []byte, seq uint16, ts uint32, ssrc uint32, marker bool) {
	if len(payload) < RTPHeaderSize {
		return
	}
	h := rtp.Header{
		Version:        2,
		PayloadType:    96,
		SequenceNumber: seq,
		Timestamp:      ts,
		SSRC:           ssrc,
		Marker:         marker,
	}
	if _, err := h.MarshalTo(payload); err != nil {
		// Header.MarshalTo only fails on a short buffer, which is checked above.
		return
	}
}
