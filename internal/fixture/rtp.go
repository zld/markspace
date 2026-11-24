package fixture

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/pion/rtp"
)

// rtpRecord is one line of the network return-feed sidecar. It carries the
// raw 32-bit RTP timestamp so the repair stage can model wrap boundaries with
// pion's wire types instead of guessing.
type rtpRecord struct {
	Seq       uint16 `json:"seq"`
	Timestamp uint32 `json:"timestamp"`
	SSRC      uint32 `json:"ssrc"`
	CaptureNs int64  `json:"capture_ns"`
	Marker    bool   `json:"marker"`
}

// RTPClockRate is the RTP timestamp clock for the sidecar.
const RTPClockRate = 90000

// writeRTP emits a JSONL sidecar. With wrap=true the 32-bit timestamp is
// driven across the wrap boundary shortly before the end so the repair stage
// must extend it with the RTP extension machinery instead of using it raw.
func writeRTP(path string, t *Table, startNs int64, wrap bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriterSize(f, 64*1024)
	enc := json.NewEncoder(bw)
	perFrame := float64(RTPClockRate) / t.Spec.FPS
	// Start just below the 32-bit wrap point so a clean run never wraps but a
	// corrupted one crosses it.
	// The wrap run crosses the 32-bit boundary two thirds of the way through,
	// so the extension must carry exact ticks across the discontinuity. The
	// clean run starts far from the boundary.
	base := uint32(t.Spec.Seed*977 + 0x1234_5678)
	if wrap {
		base = 0xFFFF_FFFF - uint32(perFrame*float64(len(t.Samples))*(2.0/3.0))
	}
	ssrc := uint32(t.Spec.Seed) ^ 0xA5A5_0001
	for i := range t.Samples {
		ts := base + uint32(float64(i)*perFrame)
		rec := rtpRecord{
			Seq:       uint16(i & 0xFFFF),
			Timestamp: ts,
			SSRC:      ssrc,
			CaptureNs: startNs + t.Samples[i].DTS*int64(time.Second)/int64(t.Spec.Timescale),
			Marker:    t.Samples[i].Sync,
		}
		if err := enc.Encode(rec); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// ReadRTP is a test/demo helper that loads the sidecar.
func ReadRTP(path string) ([]rtpRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []rtpRecord
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var r rtpRecord
		if err := dec.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Packet builds a pion RTP packet for a record so tests can marshal it and
// reuse pion's header extension profile handling.
func (r rtpRecord) Packet(payloadType uint8) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         r.Marker,
			SequenceNumber: r.Seq,
			Timestamp:      r.Timestamp,
			SSRC:           r.SSRC,
		},
		Payload: []byte{payloadType},
	}
}
