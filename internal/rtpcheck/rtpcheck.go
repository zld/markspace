// Package rtpcheck performs auxiliary structural validation of MPEG-TS over
// RTP packetization using github.com/pion/rtp. It groups 188-byte TS packets
// into 7-packet datagrams (the common 1316-byte MP2T payload), round-trips
// each through pion/rtp marshal/unmarshal, and verifies sequence continuity.
package rtpcheck

import (
	"fmt"

	"github.com/pion/rtp"
)

const (
	// TSPayloadType is the RTP static payload type for MP2T.
	TSPayloadType uint8 = 33
	// PacketSize is MPEG-TS packet size.
	PacketSize = 188
	// TSPacketsPerDatagram is the conventional TS-per-RTP grouping.
	TSPacketsPerDatagram = 7
)

// Result summarizes a validation pass.
type Result struct {
	Datagrams    int
	TSPackets    int
	MaxPayload   int
	SequenceFrom uint16
	SequenceTo   uint16
}

// Validate packetizes tsBytes and verifies every RTP packet round-trips.
// rtpTimestamp is the shared clock value for the interval boundary.
func Validate(tsBytes []byte, ssrc uint32, baseSeq uint16, rtpTimestamp uint32) (Result, error) {
	var res Result
	if len(tsBytes)%PacketSize != 0 {
		return res, fmt.Errorf("ts payload length %d not a multiple of 188", len(tsBytes))
	}
	res.TSPackets = len(tsBytes) / PacketSize
	chunk := TSPacketsPerDatagram * PacketSize
	seq := baseSeq
	first := seq
	for off := 0; off < len(tsBytes); off += chunk {
		end := off + chunk
		last := false
		if end >= len(tsBytes) {
			end = len(tsBytes)
			last = true
		}
		payload := tsBytes[off:end]
		if len(payload)%PacketSize != 0 {
			return res, fmt.Errorf("chunk at %d not TS aligned", off)
		}
		if len(payload) > res.MaxPayload {
			res.MaxPayload = len(payload)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         last,
				PayloadType:    TSPayloadType,
				SequenceNumber: seq,
				Timestamp:      rtpTimestamp,
				SSRC:           ssrc,
			},
			Payload: payload,
		}
		buf := make([]byte, 12+len(payload)+16)
		n, err := pkt.MarshalTo(buf)
		if err != nil {
			return res, fmt.Errorf("marshal seq %d: %w", seq, err)
		}
		var back rtp.Packet
		if err := back.Unmarshal(buf[:n]); err != nil {
			return res, fmt.Errorf("unmarshal seq %d: %w", seq, err)
		}
		if back.Header.Version != 2 || back.Header.PayloadType != TSPayloadType {
			return res, fmt.Errorf("rtp header mismatch at seq %d", seq)
		}
		if back.Header.SequenceNumber != seq || back.Header.Timestamp != rtpTimestamp {
			return res, fmt.Errorf("rtp seq/timestamp mismatch at %d", seq)
		}
		if string(back.Payload) != string(payload) {
			return res, fmt.Errorf("rtp payload round-trip mismatch at seq %d", seq)
		}
		seq++
		res.Datagrams++
	}
	res.SequenceFrom = first
	res.SequenceTo = seq - 1
	return res, nil
}
