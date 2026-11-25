// Package ingest turns on-disk RTP logs and container metadata into the
// in-memory per-representation streams the pipeline operates on.
package ingest

import (
	"io"
	"time"

	"github.com/pion/rtp"

	"rtpreplay/clock"
	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
)

// Packet couples a parsed RTP packet with its record position and its
// position on the representation's unwrapped clock.
type Packet struct {
	RecordIndex int
	Offset      int64
	Arrival     time.Time
	RTP         *rtp.Packet
	Clock       *clock.Packet
}

// Stream is one representation (one bitrate tier) of a replay track. Every
// stream owns its own unwrapper and time base: recovery state must never be
// shared between representations.
type Stream struct {
	SSRC        uint32
	ClockRate   uint32
	BitrateKbps int
	Packets     []*Packet
	TimeBase    media.TimeBase
	DriftPPM    float64
	Unwrapper   *clock.Unwrapper

	// Duplicates records record indices whose RTP sequence repeated.
	Duplicates []int
}

// ParseRTPStream reads a complete rtplog, unmarshals every RTP packet with
// pion/rtp and feeds a representation-local unwrapper. Drift is estimated
// from healthy (non-isolated) packets against their arrival times.
func ParseRTPStream(r io.Reader, size int64, rate uint32, bitrateKbps int) (*Stream, error) {
	if !media.SupportedClockRate(rate) {
		return nil, media.NewError(media.KindUnsupported, "UNSUPPORTED_RATE", "rtp", -1,
			"clock rate %d is outside the sample scope", rate)
	}
	reader, err := rtplog.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	uw, err := clock.New(rate)
	if err != nil {
		return nil, err
	}
	s := &Stream{
		ClockRate:   rate,
		BitrateKbps: bitrateKbps,
		Unwrapper:   uw,
		TimeBase: media.TimeBase{
			ClockRate: rate,
		},
	}

	var driftPoints []media.DriftPoint
	var baseArrival time.Time
	var baseTicks int64
	for {
		rec, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		var pkt rtp.Packet
		if uerr := pkt.Unmarshal(rec.Packet); uerr != nil {
			return nil, media.NewError(media.KindBadInput, "RTP_UNMARSHAL_FAILED", "rtp",
				rec.Offset, "record %d: %v", rec.Index, uerr)
		}
		cp, aerr := uw.Add(rec.Index, uint32(pkt.SequenceNumber), pkt.Timestamp, rec.Arrival)
		if aerr != nil {
			return nil, aerr
		}
		if len(s.Packets) == 0 {
			s.SSRC = pkt.SSRC
			s.TimeBase.SSRC = pkt.SSRC
			s.TimeBase.FirstSeq = pkt.SequenceNumber
			s.TimeBase.BaseRTP = pkt.Timestamp
			s.TimeBase.BaseArrival = rec.Arrival
			baseArrival = rec.Arrival
			baseTicks = cp.Unwrapped
		}
		if pkt.SSRC != s.SSRC {
			return nil, media.NewError(media.KindBadInput, "SSRC_CHANGED", "rtp", rec.Offset,
				"record %d: SSRC %d != stream SSRC %d", rec.Index, pkt.SSRC, s.SSRC)
		}
		if cp.Anomaly == clock.AnomDuplicate {
			s.Duplicates = append(s.Duplicates, rec.Index)
		}
		if !cp.Isolated {
			driftPoints = append(driftPoints, media.DriftPoint{
				Arrival:  rec.Arrival.Sub(baseArrival),
				RTPTicks: cp.Unwrapped - baseTicks,
			})
		}
		s.Packets = append(s.Packets, &Packet{
			RecordIndex: rec.Index,
			Offset:      rec.Offset,
			Arrival:     rec.Arrival,
			RTP:         &pkt,
			Clock:       cp,
		})
	}
	uw.Finish()
	if len(s.Packets) == 0 {
		return nil, media.NewError(media.KindBadInput, "EMPTY_STREAM", "rtp", -1, "no RTP records")
	}
	s.TimeBase.Samples = len(driftPoints)
	s.TimeBase.DriftPPM = media.EstimateDrift(driftPoints, rate)
	s.DriftPPM = s.TimeBase.DriftPPM
	s.TimeBase.Cycles = countCycles(s.Packets)
	return s, nil
}

func countCycles(pkts []*Packet) uint32 {
	if len(pkts) == 0 {
		return 0
	}
	first := uint32(pkts[0].RTP.SequenceNumber)
	last := uint32(pkts[len(pkts)-1].RTP.SequenceNumber)
	if ext := pkts[len(pkts)-1].Clock.ExtSeq; ext > 0 {
		return uint32(ext / 65536)
	}
	if last < first {
		return 1
	}
	return 0
}

// HealthyPackets returns packets outside isolated anomaly regions.
func (s *Stream) HealthyPackets() []*Packet {
	out := make([]*Packet, 0, len(s.Packets))
	for _, p := range s.Packets {
		if !p.Clock.Isolated {
			out = append(out, p)
		}
	}
	return out
}

// IsolatedRanges exposes the unwrapper's anomaly ranges.
func (s *Stream) IsolatedRanges() []clock.Range {
	return s.Unwrapper.IsolatedRanges()
}
