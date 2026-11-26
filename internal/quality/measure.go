// Package quality turns parsed ingest records into quantified track
// measurements and per-track conversion configurations. All thresholds
// are explicit integers; nothing is decided implicitly.
package quality

import (
	"fmt"

	"pwf/internal/ingest"
	"pwf/internal/media"
)

// Measurement is the quantified quality profile of one parsed track.
type Measurement struct {
	Track          string
	Codec          string
	ClockHz        uint32
	PacketCount    int    // accepted, de-duplicated packets
	DupCount       int    // repeated packets observed
	OutOfOrder     int    // packets arriving below the running high mark
	GapCount       int    // number of distinct missing ranges
	MissingPackets int    // total missing sequence numbers
	MaxGap         uint64 // largest single gap, in packets
	FirstGapSeq    uint64 // extended seq where the first gap starts
	TotalTicks     int64
	Timescale      uint32
	TotalDuration  media.Duration
	PayloadBytes   int64
	AvgBitrateBps  int64    // payload bits per measured second, rounded down
	AnomalyRatio   Rational // (dup + missing + out-of-order) / total expected
}

// Rational is an exact fraction used for threshold comparisons and
// reports, avoiding float accumulation.
type Rational struct {
	Num int64
	Den int64
}

func (r Rational) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// LessThan reports r < other by cross multiplication (denominators > 0).
func (r Rational) LessThan(other Rational) bool {
	return r.Num*other.Den < other.Num*r.Den
}

// MeasureRTP computes the Measurement of a parsed RTP stream.
func MeasureRTP(res *ingest.RTPStreamResult) (*Measurement, error) {
	if len(res.Records) == 0 {
		return nil, media.NewError(media.KindCorrupt, media.StageMeasure,
			"no accepted records to measure", nil).WithTrack(res.Track)
	}
	tb, err := media.NewTimeBase(res.ClockHz)
	if err != nil {
		return nil, media.NewError(media.KindUndetermined, media.StageMeasure,
			"invalid RTP clock", err).WithTrack(res.Track)
	}
	m := &Measurement{
		Track: res.Track, Codec: res.Codec, ClockHz: res.ClockHz,
		PacketCount: len(res.Records), DupCount: res.DupCount, OutOfOrder: res.OutOfOrder,
		GapCount: len(res.Gaps), Timescale: res.ClockHz,
	}
	total := media.Zero(tb)
	var bytes int64
	for _, rec := range res.Records {
		sum, err := total.Add(rec.Duration)
		if err != nil {
			return nil, media.NewError(media.KindUndetermined, media.StageMeasure,
				"duration accumulation failed", err).WithTrack(res.Track)
		}
		total = sum
		bytes += int64(len(rec.Payload))
	}
	m.TotalDuration = total
	m.TotalTicks = total.Ticks
	m.PayloadBytes = bytes

	for _, g := range res.Gaps {
		m.MissingPackets += int(g.Count)
		if g.Count > m.MaxGap {
			m.MaxGap = g.Count
		}
		if m.FirstGapSeq == 0 || g.StartSeq < m.FirstGapSeq {
			m.FirstGapSeq = g.StartSeq
		}
	}
	expected := int64(m.PacketCount + m.MissingPackets)
	if expected == 0 {
		expected = 1
	}
	anomalies := int64(m.MissingPackets + m.DupCount + m.OutOfOrder)
	m.AnomalyRatio = Rational{Num: anomalies, Den: expected}
	if total.Ticks > 0 {
		// bits/s = bytes*8 * clock / ticks (exact integer, rounded down).
		m.AvgBitrateBps = bytes * 8 * int64(res.ClockHz) / total.Ticks
	}
	return m, nil
}

// MeasureMP4 computes the Measurement of a parsed MP4 audio track.
func MeasureMP4(res *ingest.MP4TrackResult) (*Measurement, error) {
	if len(res.Samples) == 0 {
		return nil, media.NewError(media.KindCorrupt, media.StageMeasure,
			"no samples to measure", nil).WithTrack(res.Track)
	}
	tb, err := media.NewTimeBase(res.Timescale)
	if err != nil {
		return nil, media.NewError(media.KindUndetermined, media.StageMeasure,
			"invalid track timescale", err).WithTrack(res.Track)
	}
	total := media.Zero(tb)
	var bytes int64
	for _, s := range res.Samples {
		sum, err := total.Add(s.Duration)
		if err != nil {
			return nil, media.NewError(media.KindUndetermined, media.StageMeasure,
				"sample duration accumulation failed", err).WithTrack(res.Track)
		}
		total = sum
		bytes += int64(s.Size)
	}
	m := &Measurement{
		Track: res.Track, Codec: res.Codec, ClockHz: res.Timescale,
		PacketCount: len(res.Samples), Timescale: res.Timescale,
		TotalDuration: total, TotalTicks: total.Ticks, PayloadBytes: bytes,
		AnomalyRatio: Rational{Num: 0, Den: int64(len(res.Samples))},
	}
	if total.Ticks > 0 {
		m.AvgBitrateBps = bytes * 8 * int64(res.Timescale) / total.Ticks
	}
	return m, nil
}
