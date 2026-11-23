package ingest

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"mediapipe/internal/fault"
	"mediapipe/internal/limits"
	"mediapipe/internal/model"
)

// RTPResult is the output of capture-file ingestion.
type RTPResult struct {
	Track       *model.Track
	Wraps       int
	Quarantined int
	DriftPPM    float64
}

// rtpRecord is one length-prefixed packet read from the capture file.
type rtpRecord struct {
	pkt    *rtp.Packet
	offset int64
}

// ReadRTPCapture parses a 2-byte-length-prefixed RTP capture.
func ReadRTPCapture(path, trackID string, clockRate uint32,
	errs *fault.Batch) (*RTPResult, error) {
	data, ferr := readInput(path)
	if ferr != nil {
		errs.Add(ferr)
		return nil, ferr
	}
	records, ferr2 := readRecords(path, data, errs)
	if ferr2 != nil {
		return nil, ferr2
	}
	return buildRTPTrack(path, trackID, clockRate, records, errs), nil
}

func readRecords(path string, data []byte, errs *fault.Batch) ([]rtpRecord, *fault.Error) {
	var recs []rtpRecord
	pos := 0
	for pos < len(data) {
		if len(data)-pos < 2 {
			errs.Add(fault.New(fault.KindTruncated,
				"capture length prefix truncated", path, int64(pos), true))
			return recs, nil
		}
		n := int(binary.BigEndian.Uint16(data[pos:]))
		start := pos + 2
		if n == 0 || start+n > len(data) {
			errs.Add(fault.New(fault.KindTruncated,
				fmt.Sprintf("RTP record declares %d bytes, %d remain",
					n, len(data)-start), path, int64(pos), true))
			return recs, nil
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(data[start : start+n]); err != nil {
			errs.Add(fault.New(fault.KindInvalidInput,
				"RTP unmarshal failed: "+err.Error(),
				path, int64(pos), true))
			pos = start + n
			continue
		}
		recs = append(recs, rtpRecord{pkt: pkt, offset: int64(pos)})
		pos = start + n
		if len(recs) > limits.MaxPackets {
			return recs, limits.Exceeded(path, "RTP packet count",
				int64(pos), len(recs), limits.MaxPackets)
		}
	}
	return recs, nil
}

// unwrapTS expands a 32-bit timestamp to 64 bits given the previous fully
// expanded value. A raw value that decreases while the modular step stays
// within the normal per-packet range is a forward 32-bit wrap; a large
// backwards move is a genuine jump reported separately.
func unwrapTS(prev64 int64, raw uint32, maxStep uint32) (int64, bool, bool) {
	prevLow := uint32(prev64)
	delta := int32(raw - prevLow)
	switch {
	case delta >= 0:
		// Monotonic in the 32-bit domain: either an ordinary step, or — when
		// the raw value wrapped down but the modular subtraction yields a
		// small positive delta — a forward rollover.
		if raw < prevLow && int64(delta) <= int64(maxStep)*2 {
			return prev64 + int64(delta) + (1 << 32), true, false
		}
		return prev64 + int64(delta), false, false
	case int64(-delta) <= int64(maxStep) * 2:
		// Small negative modular delta crossing the ceiling.
		return prev64 + int64(delta) + (1 << 32), true, false
	default:
		// Large backwards move with no plausible rollover explanation.
		return prev64 + int64(delta), false, true
	}
}

// rtpCommitted is a packet accepted onto the committed timeline.
type rtpCommitted struct {
	seq     uint16
	ts64    int64
	offset  int64
	payload []byte
}

// buildRTPTrack commits packets in arrival order, unwraps timestamps,
// estimates drift and quarantines late arrivals after commit.
func buildRTPTrack(path, trackID string, clockRate uint32,
	records []rtpRecord, errs *fault.Batch) *RTPResult {
	tr := &model.Track{
		ID: trackID, Kind: model.KindAudio, Timescale: clockRate,
		Container: model.ContainerRTP, SourceFile: path,
	}
	if len(records) == 0 {
		return &RTPResult{Track: tr}
	}
	var accepted []rtpRecord
	seenSeq := map[uint16]bool{}
	var quarantined int
	for _, rec := range records {
		// A sequence number already observed is a duplicate or a late
		// retransmission: quarantine it and never let it touch the clock.
		if seenSeq[rec.pkt.SequenceNumber] {
			quarantined++
			errs.Add(fault.New(fault.KindLatePacket,
				fmt.Sprintf("late/duplicate seq=%d quarantined",
					rec.pkt.SequenceNumber),
				path, rec.offset, true))
			continue
		}
		seenSeq[rec.pkt.SequenceNumber] = true
		accepted = append(accepted, rec)
	}
	// First-seen out-of-order packets are retained. Sorting by sequence number
	// restores the sender's packet order before clock expansion, so a delivery
	// reordering can never masquerade as a timestamp wrap or backwards jump.
	sort.SliceStable(accepted, func(i, j int) bool {
		return accepted[i].pkt.SequenceNumber < accepted[j].pkt.SequenceNumber
	})

	var done []rtpCommitted
	var wraps int
	var prev64 int64
	// Bounded per-packet step used to distinguish wraps from backwards jumps.
	const maxStep = 960 * 2
	for i, rec := range accepted {
		p := rec.pkt
		if i == 0 {
			prev64 = int64(p.Timestamp)
		}
		expanded, wrapped, backwards := unwrapTS(prev64, p.Timestamp, maxStep)
		if wrapped {
			wraps++
			errs.Add(fault.New(fault.KindTimestampWrap,
				"RTP 32-bit timestamp wrap",
				path, rec.offset, true))
		}
		if backwards {
			errs.Add(fault.New(fault.KindTimestampWrap,
				"RTP timestamp backwards jump before re-wrap",
				path, rec.offset, true))
		}
		prev64 = expanded
		done = append(done, rtpCommitted{
			seq: p.SequenceNumber, ts64: expanded,
			offset: rec.offset, payload: p.Payload,
		})
	}

	driftPPM := estimateDrift(done)
	tr.ClockPpm = driftPPM
	toNanos := func(ts int64) int64 {
		return ts * 1_000_000_000 / int64(clockRate)
	}
	for i, c := range done {
		dur := int64(0)
		if i+1 < len(done) {
			dur = toNanos(done[i+1].ts64 - c.ts64)
			if dur < 0 {
				dur = 0
			}
		}
		tr.Samples = append(tr.Samples, model.Sample{
			DTS: toNanos(c.ts64), PTS: toNanos(c.ts64),
			DurationNanos: dur, PayloadLen: int64(len(c.payload)),
			PayloadOff: c.offset,
		})
	}
	return &RTPResult{
		Track: tr, Wraps: wraps,
		Quarantined: quarantined, DriftPPM: driftPPM,
	}
}

// estimateDrift measures clock drift in parts per million against the nominal
// packet interval. The stream is split at wrap/backwards-jump discontinuities
// and drift is measured over each contiguous segment's full span, where
// accumulated drift survives integer rounding of individual steps. Segment
// estimates are length-weighted into the final PPM figure.
func estimateDrift(done []rtpCommitted) float64 {
	if len(done) < 4 {
		return 0
	}
	nominal := nominalStep(done)
	if nominal <= 0 {
		return 0
	}
	var weighted, weightSum float64
	segStart := 0
	flush := func(end int) {
		span := end - segStart
		if span < 4 {
			return
		}
		dt := done[end].ts64 - done[segStart].ts64
		expected := int64(span) * nominal
		if expected <= 0 {
			return
		}
		ppm := float64(dt-expected) / float64(expected) * 1e6
		w := float64(span)
		weighted += ppm * w
		weightSum += w
	}
	for i := 1; i < len(done); i++ {
		d := done[i].ts64 - done[i-1].ts64
		if d <= 0 || d >= (1<<31) {
			flush(i - 1)
			segStart = i
		}
	}
	flush(len(done) - 1)
	if weightSum == 0 {
		return 0
	}
	return weighted / weightSum
}

func nominalStep(done []rtpCommitted) int64 {
	if len(done) < 2 {
		return 960
	}
	// Median per-packet timestamp step. Steps spanning a 32-bit wrap or a
	// backwards jump are excluded so the nominal interval is not distorted.
	var steps []int64
	for i := 1; i < len(done); i++ {
		d := done[i].ts64 - done[i-1].ts64
		if d > 0 && d < (1<<31) {
			steps = append(steps, d)
		}
	}
	if len(steps) == 0 {
		return 960
	}
	return int64(median(float64Slice(steps)))
}

func median(xs []float64) float64 {
	cp := append([]float64(nil), xs...)
	for i := 0; i < len(cp); i++ {
		for j := i + 1; j < len(cp); j++ {
			if cp[j] < cp[i] {
				cp[i], cp[j] = cp[j], cp[i]
			}
		}
	}
	m := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[m]
	}
	return (cp[m-1] + cp[m]) / 2
}

func float64Slice(xs []int64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = float64(x)
	}
	return out
}
