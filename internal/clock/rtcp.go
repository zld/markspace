// Package clock also hosts RTCP sender-report handling: Sender Reports anchor
// an RTP media clock to wall time, letting callers estimate how far a device
// clock has drifted from the reference. Implausible jumps are quarantined so
// they never contaminate the committed media timeline.
package clock

import (
	"encoding/binary"
	"fmt"

	"example.com/mediaforge/internal/merrors"
)

// MaxRTCPSize bounds an RTCP compound packet accepted by the parser.
const MaxRTCPSize = 1500

// rtcpSRMinLen is the smallest Sender Report: 4 header + 4 SSRC + 8 NTP +
// 4 RTP timestamp + 4 packet count + 4 octet count.
const rtcpSRMinLen = 28

// SenderReport is the subset of an RTCP SR this package needs.
type SenderReport struct {
	SSRC        uint32
	NTPSeconds  uint32
	NTPFraction uint32
	RTPTime     uint32
	PacketCnt   uint32
	OctetCnt    uint32
}

// NTPTicks returns the NTP timestamp as 32.32 fixed point in the upper bits,
// i.e. seconds in the high word and fraction in the low word.
func (s SenderReport) NTPTicks() uint64 {
	return uint64(s.NTPSeconds)<<32 | uint64(s.NTPFraction)
}

// NTPMicros converts the NTP stamp into microseconds since the NTP epoch.
func (s SenderReport) NTPMicros() uint64 {
	return uint64(s.NTPSeconds)*1_000_000 + (uint64(s.NTPFraction)*1_000_000)>>32
}

// BuildSenderReport serialises a single Sender Report RTCP packet.
func BuildSenderReport(sr SenderReport) []byte {
	b := make([]byte, rtcpSRMinLen)
	b[0] = 0x80 // version 2, no padding, no report count
	b[1] = 200
	binary.BigEndian.PutUint16(b[2:4], uint16(rtcpSRMinLen/4-1))
	binary.BigEndian.PutUint32(b[4:8], sr.SSRC)
	binary.BigEndian.PutUint32(b[8:12], sr.NTPSeconds)
	binary.BigEndian.PutUint32(b[12:16], sr.NTPFraction)
	binary.BigEndian.PutUint32(b[16:20], sr.RTPTime)
	binary.BigEndian.PutUint32(b[20:24], sr.PacketCnt)
	binary.BigEndian.PutUint32(b[24:28], sr.OctetCnt)
	return b
}

// NewSenderReport builds a report from integer microseconds since the NTP
// epoch and a raw RTP timestamp.
func NewSenderReport(ssrc uint32, ntpMicros uint64, rtpTime uint32) SenderReport {
	secs := ntpMicros / 1_000_000
	frac := ((ntpMicros % 1_000_000) << 32) / 1_000_000
	return SenderReport{
		SSRC:        ssrc,
		NTPSeconds:  uint32(secs),
		NTPFraction: uint32(frac),
		RTPTime:     rtpTime,
	}
}

// ParseSenderReport extracts the first Sender Report from an RTCP packet.
// Other packet types in a compound packet are skipped. Every error carries
// the RTCP stage, a category, and the byte offset of the offending field.
func ParseSenderReport(buf []byte) (SenderReport, error) {
	if len(buf) > MaxRTCPSize {
		return SenderReport{}, merrors.At(merrors.StageRTPParse, merrors.Bounds, int64(len(buf)),
			"rtcp packet size %d exceeds limit", len(buf))
	}
	if len(buf) < 4 {
		return SenderReport{}, merrors.At(merrors.StageRTPParse, merrors.Truncated, int64(len(buf)),
			"rtcp packet shorter than header")
	}
	off := 0
	for off+4 <= len(buf) {
		pt := buf[off+1]
		lengthWords := int(binary.BigEndian.Uint16(buf[off+2 : off+4]))
		pktLen := (lengthWords + 1) * 4
		if pktLen < 4 || off+pktLen > len(buf) {
			return SenderReport{}, merrors.At(merrors.StageRTPParse, merrors.Truncated, int64(off),
				"rtcp packet length %d overruns buffer %d", pktLen, len(buf)-off)
		}
		if pt == 200 { // Sender Report
			return parseSRBody(buf[off:off+pktLen], off)
		}
		off += pktLen
	}
	return SenderReport{}, merrors.At(merrors.StageRTPParse, merrors.Malformed, int64(off),
		"no sender report present")
}

func parseSRBody(b []byte, base int) (SenderReport, error) {
	if len(b) < rtcpSRMinLen {
		return SenderReport{}, merrors.At(merrors.StageRTPParse, merrors.Truncated, int64(base),
			"sender report %d bytes, need %d", len(b), rtcpSRMinLen)
	}
	if b[0]>>6 != 2 {
		return SenderReport{}, merrors.At(merrors.StageRTPParse, merrors.Malformed, int64(base),
			"rtcp version %d", b[0]>>6)
	}
	return SenderReport{
		SSRC:        binary.BigEndian.Uint32(b[4:8]),
		NTPSeconds:  binary.BigEndian.Uint32(b[8:12]),
		NTPFraction: binary.BigEndian.Uint32(b[12:16]),
		RTPTime:     binary.BigEndian.Uint32(b[16:20]),
		PacketCnt:   binary.BigEndian.Uint32(b[20:24]),
		OctetCnt:    binary.BigEndian.Uint32(b[24:28]),
	}, nil
}

// DriftEstimator estimates the ppm offset between a device RTP clock and the
// shared reference clock using Sender Reports. The first accepted report
// becomes the baseline; every later sample is compared against it so that
// random per-report jitter does not accumulate.
//
// A sample that implies an implausible jump is quarantined: it is not folded
// into the estimate, the estimator enters isolation, and an OutOfRange error
// is returned. If two consecutive rejected samples agree with each other the
// device has genuinely re-anchored, so the estimator re-baselines on the
// second one and leaves isolation.
type DriftEstimator struct {
	clockRate uint32

	haveBase bool
	base     anchored

	haveCandidate bool
	candidate     anchored
	candidatePPM  int64

	lastPPM  int64
	havePPM  bool
	isolated bool
	samples  int
	rejected int
}

type anchored struct {
	rtpExt    uint64 // RTP timestamp on the extended timeline
	ntpMicros uint64 // NTP wall time from the same SR
}

// NewDriftEstimator creates an estimator for a stream clock rate.
func NewDriftEstimator(clockRate uint32) (*DriftEstimator, error) {
	if clockRate == 0 {
		return nil, merrors.New(merrors.StageTimeline, merrors.Invalid, "clock rate must be positive")
	}
	return &DriftEstimator{clockRate: clockRate}, nil
}

// maxPlausiblePPM bounds a drift jump. Real device clocks sit within a few
// hundred ppm; anything beyond 1% is either a mislabelled SR or corruption.
const maxPlausiblePPM = 10_000

// reanchorTolerancePPM is how close a rejected sample must be to the prior
// rejected sample for the pair to count as a genuine re-anchor.
const reanchorTolerancePPM = 500

// ppmBetween computes the ppm offset between two anchors.
func (d *DriftEstimator) ppmBetween(a, b anchored) (int64, error) {
	if b.ntpMicros <= a.ntpMicros || b.rtpExt <= a.rtpExt {
		return 0, merrors.At(merrors.StageTimeline, merrors.OutOfRange, int64(b.ntpMicros),
			"non-advancing sender report (rtp=%d ntp=%d)", b.rtpExt, b.ntpMicros)
	}
	ntpDelta := b.ntpMicros - a.ntpMicros
	rtpDelta := b.rtpExt - a.rtpExt
	expected := ntpDelta * uint64(d.clockRate) / 1_000_000
	if expected == 0 {
		return 0, merrors.At(merrors.StageTimeline, merrors.OutOfRange, int64(b.ntpMicros),
			"sender reports too close together")
	}
	diff := int64(rtpDelta) - int64(expected)
	return diff * 1_000_000 / int64(expected), nil
}

// Observe feeds one Sender Report and the extended RTP timestamp of its
// media clock. It returns the current drift estimate in ppm.
func (d *DriftEstimator) Observe(sr SenderReport, rtpExt uint64) (int64, error) {
	cur := anchored{rtpExt: rtpExt, ntpMicros: sr.NTPMicros()}
	if !d.haveBase {
		d.base = cur
		d.haveBase = true
		d.isolated = false
		return 0, nil
	}

	ppm, err := d.ppmBetween(d.base, cur)
	if err == nil && ppm >= -maxPlausiblePPM && ppm <= maxPlausiblePPM {
		d.lastPPM = ppm
		d.havePPM = true
		d.samples++
		d.isolated = false
		d.haveCandidate = false
		return ppm, nil
	}

	// Implausible or non-advancing: quarantine it.
	d.rejected++
	d.isolated = true
	if err == nil && d.haveCandidate {
		if prev, cerr := d.ppmBetween(d.candidate, cur); cerr == nil &&
			prev >= -reanchorTolerancePPM && prev <= reanchorTolerancePPM {
			// Two agreeing outliers in a row: the device really did
			// re-anchor. Accept the new baseline rather than quarantining
			// every future report forever.
			d.base = cur
			d.lastPPM = 0
			d.havePPM = true
			d.isolated = false
			d.haveCandidate = false
			d.samples++
			return d.lastPPM, nil
		}
	}
	d.candidate = cur
	d.haveCandidate = true
	return d.lastPPM, merrors.At(merrors.StageTimeline, merrors.OutOfRange, int64(cur.ntpMicros),
		"implausible drift %d ppm (rtp=%d ntp=%d); isolating", ppm, cur.rtpExt, cur.ntpMicros)
}

// PPM returns the most recent accepted drift estimate.
func (d *DriftEstimator) PPM() (int64, bool) { return d.lastPPM, d.havePPM }

// Isolated reports whether the estimator is currently quarantining reports.
func (d *DriftEstimator) Isolated() bool { return d.isolated }

// Samples returns accepted and rejected report counts.
func (d *DriftEstimator) Samples() (accepted, rejected int) { return d.samples, d.rejected }

// String renders estimator state for diagnostics.
func (d *DriftEstimator) String() string {
	return fmt.Sprintf("drift=%dppm accepted=%d rejected=%d isolated=%v",
		d.lastPPM, d.samples, d.rejected, d.isolated)
}
