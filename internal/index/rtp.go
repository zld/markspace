// RTP carriage of keyframe index events. One packet carries exactly one
// self-delimiting event, so packet boundaries can be validated independently
// of the payload contents.
package index

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pion/rtp"
)

// EventsToRTP carries one index event per RTP packet. Each packet is marked
// and self-delimiting so packet boundaries can be verified independently.
func EventsToRTP(ix Index, ssrc uint32, startSeq uint16) ([]*rtp.Packet, error) {
	if ix.Timescale == 0 {
		return nil, errors.New("index: timescale required for RTP carriage")
	}
	pkts := make([]*rtp.Packet, 0, len(ix.Entries))
	for i, e := range ix.Entries {
		payload := []byte(fmt.Sprintf("%s|%d|%d|%d", ix.Layer, ix.Timescale, e.PTS, e.Offset))
		pkts = append(pkts, &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    97,
				Marker:         true,
				SequenceNumber: startSeq + uint16(i),
				Timestamp:      uint32(e.PTS),
				SSRC:           ssrc,
			},
			Payload: payload,
		})
	}
	return pkts, nil
}

// EventsFromRTP validates packet boundaries and rebuilds the index. A payload
// that does not contain exactly one complete event, a missing marker bit, or a
// sequence gap is rejected rather than silently accepted.
func EventsFromRTP(pkts []*rtp.Packet) (Index, error) {
	var ix Index
	if len(pkts) == 0 {
		return ix, errors.New("index: no RTP packets")
	}
	for i, p := range pkts {
		if p == nil {
			return ix, fmt.Errorf("index: nil packet at %d", i)
		}
		if !p.Marker {
			return ix, fmt.Errorf("index: packet %d lacks the event marker bit", i)
		}
		if i > 0 && p.SequenceNumber != pkts[i-1].SequenceNumber+1 {
			return ix, fmt.Errorf("index: sequence gap at packet %d: %d -> %d", i, pkts[i-1].SequenceNumber, p.SequenceNumber)
		}
		parts := strings.Split(string(p.Payload), "|")
		if len(parts) != 4 {
			return ix, fmt.Errorf("index: packet %d payload has %d fields, want 4", i, len(parts))
		}
		ts, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil || ts == 0 {
			return ix, fmt.Errorf("index: packet %d invalid timescale", i)
		}
		pts, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return ix, fmt.Errorf("index: packet %d invalid pts", i)
		}
		off, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			return ix, fmt.Errorf("index: packet %d invalid offset", i)
		}
		if i == 0 {
			ix.Layer, ix.Timescale = parts[0], uint32(ts)
		} else if ix.Layer != parts[0] || ix.Timescale != uint32(ts) {
			return ix, fmt.Errorf("index: packet %d disagrees on layer/timescale", i)
		}
		ix.Entries = append(ix.Entries, Entry{PTS: pts, Offset: off})
	}
	return ix, nil
}
