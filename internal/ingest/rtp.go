package ingest

import (
	"fmt"

	"github.com/pion/rtp"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/trace"
)

// RTPClock converts 32-bit RTP media timestamps to the unified nanosecond
// base. It extends wraparound and interprets sequence numbers.
type RTPClock struct {
	TrackID       int
	ClockRate     uint32
	OriginNS      model.NS
	haveTimestamp bool
	lastTimestamp uint32
	lastExtended  uint64
	haveSequence  bool
	lastSequence  uint16
}

// PacketTime is the result of reading one RTP packet.
type PacketTime struct {
	TimestampNS       model.NS `json:"timestamp_ns"`
	ExtendedTimestamp uint64   `json:"extended_timestamp"`
	Sequence          uint16   `json:"sequence"`
	SequenceDelta     int32    `json:"sequence_delta"`
}

// NewRTPClock creates a clock for one RTP track.
func NewRTPClock(trackID int, clockRate uint32, originNS model.NS) (*RTPClock, error) {
	if clockRate == 0 {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageRTP, "RTP clock rate must not be zero").WithTrack(trackID)
	}
	return &RTPClock{TrackID: trackID, ClockRate: clockRate, OriginNS: originNS}, nil
}

// ParseRTPPacket parses one raw RTP packet with github.com/pion/rtp.
func ParseRTPPacket(raw []byte) (*rtp.Packet, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageRTP, "parse RTP packet: %v", err)
	}
	return &pkt, nil
}

// Observe interprets timestamp and sequence. Modular arithmetic correctly
// crosses the 32-bit timestamp and 16-bit sequence wraparounds. A monotonicity
// violation is returned as a located TIMESTAMP_REGRESSION error.
func (c *RTPClock) Observe(pkt *rtp.Packet) (PacketTime, error) {
	if pkt == nil {
		return PacketTime{}, trace.E(trace.CodeInvalidArgument, trace.StageRTP, "nil RTP packet").WithTrack(c.TrackID)
	}

	ext := c.extendTimestamp(pkt.Header.Timestamp)
	ns, err := model.TicksToNS(ext, c.ClockRate)
	if err != nil {
		return PacketTime{}, trace.Wrap(trace.StageRTP, err).(*trace.Error).WithTrack(c.TrackID)
	}
	pt := PacketTime{
		TimestampNS:       c.OriginNS + ns,
		ExtendedTimestamp: ext,
		Sequence:          pkt.Header.SequenceNumber,
	}

	if c.haveTimestamp && ext < c.lastExtended {
		return pt, trace.E(
			trace.CodeTimestampRegression,
			trace.StageRTP,
			"RTP timestamp regressed: ext=%d after %d seq=%d",
			ext, c.lastExtended, pkt.Header.SequenceNumber,
		).WithTrack(c.TrackID).WithTime(int64(pt.TimestampNS))
	}

	pt.SequenceDelta = c.sequenceDelta(pkt.Header.SequenceNumber)
	c.lastTimestamp = pkt.Header.Timestamp
	c.lastExtended = ext
	c.haveTimestamp = true
	return pt, nil
}

func (c *RTPClock) extendTimestamp(ts uint32) uint64 {
	if !c.haveTimestamp {
		return uint64(ts)
	}
	// Signed modular difference also handles wraparound and reordered values.
	delta := int32(ts - c.lastTimestamp)
	return uint64(int64(c.lastExtended) + int64(delta))
}

func (c *RTPClock) sequenceDelta(seq uint16) int32 {
	if !c.haveSequence {
		c.lastSequence = seq
		c.haveSequence = true
		return 1
	}
	delta := int32(int16(seq - c.lastSequence))
	c.lastSequence = seq
	return delta
}

// RTPTimestampToNS is a stateless single-timestamp conversion.
func RTPTimestampToNS(timestamp uint32, clockRate uint32) (model.NS, error) {
	if clockRate == 0 {
		return 0, trace.E(trace.CodeInvalidArgument, trace.StageRTP, "RTP clock rate must not be zero")
	}
	return model.TicksToNS(uint64(timestamp), clockRate)
}

// String makes PacketTime convenient in diagnostic output.
func (p PacketTime) String() string {
	return fmt.Sprintf("seq=%d ext_ts=%d time_ns=%d delta=%d", p.Sequence, p.ExtendedTimestamp, p.TimestampNS, p.SequenceDelta)
}
