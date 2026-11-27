// Package rtp expands RTP sequence numbers into an extended, wrap-aware
// ordering and identifies duplicates, losses and truncation. It never fabricates
// media for missing packets: gaps are reported as explicit ranges.
package rtp

import (
	"errors"

	"github.com/pion/rtp"

	"mediasync/internal/model"
)

// Record is one source packet after sequence reconstruction.
type Record struct {
	Offset     int
	ExtSeq     uint64
	SSRC       uint32
	Marker     bool
	Timestamp  uint32
	Payload    []byte
	Duplicate  bool
	ParseError *model.Failure
}

// Gap is an inclusive range of extended sequence numbers that never arrived.
type Gap struct {
	From uint64
	To   uint64
}

// Result is the reconstructed view of one track's RTP stream.
type Result struct {
	TrackID      string
	Records      []Record // every received packet, sorted by (ExtSeq, Offset)
	Gaps         []Gap
	Failures     []model.Failure
	FirstExtSeq  uint64
	LastExtSeq   uint64
}

// seqMod is the RTP 16-bit sequence modulus.
const seqMod uint64 = 1 << 16

// Reconstruct validates and orders the packets of a single track. Packets are
// consumed in source order; sequence numbers are unwrapped relative to the
// stream so out-of-order delivery and 16-bit wrap are handled without floats.
func Reconstruct(trackID string, packets []model.RawPacket) (*Result, error) {
	if len(packets) > model.MaxPacketsPerTrack {
		return nil, model.NewTrackFailure(trackID, model.StageRTP, model.ErrTooManyPackets,
			"packet count "+itoa(len(packets))+" exceeds limit "+itoa(model.MaxPacketsPerTrack))
	}

	res := &Result{TrackID: trackID}
	type seen struct {
		offset int
		record *Record
	}
	known := make(map[uint64]*Record, len(packets))
	var prevExt uint64
	haveBase := false

	for _, rp := range packets {
		if len(rp.RTP) == 0 {
			res.Failures = append(res.Failures, model.NewFailure(trackID, model.StageRTP,
				rp.Offset, model.ErrEmptyPacket, "empty RTP datagram"))
			continue
		}
		if len(rp.RTP) > model.MaxPacketBytes {
			res.Failures = append(res.Failures, model.NewFailure(trackID, model.StageRTP,
				rp.Offset, model.ErrPacketTooLarge,
				"packet "+itoa(len(rp.RTP))+" bytes exceeds limit "+itoa(model.MaxPacketBytes)))
			continue
		}

		var pkt rtp.Packet
		if err := pkt.Unmarshal(rp.RTP); err != nil {
			f := model.NewFailure(trackID, model.StageRTP, rp.Offset, model.ErrTruncated,
				"RTP unmarshal failed: "+err.Error())
			res.Failures = append(res.Failures, f)
			// A datagram of at least 4 bytes still carries its sequence
			// number at offset 2, so it can occupy a sequence slot even
			// though its payload is unusable.
			if len(rp.RTP) < 4 {
				continue
			}
			seq := uint64(rp.RTP[2])<<8 | uint64(rp.RTP[3])
			ext := unwrap(seq, prevExt, haveBase)
			if !haveBase {
				res.FirstExtSeq, haveBase = ext, true
			}
			prevExt = ext
			rec := &Record{Offset: rp.Offset, ExtSeq: ext, ParseError: &f}
			if dup := known[ext]; dup != nil {
				rec.Duplicate = true
			} else {
				known[ext] = rec
			}
			res.Records = append(res.Records, *rec)
			continue
		}

		seq := uint64(pkt.SequenceNumber)
		ext := unwrap(seq, prevExt, haveBase)
		if !haveBase {
			res.FirstExtSeq, haveBase = ext, true
		}
		prevExt = ext

		rec := &Record{
			Offset:    rp.Offset,
			ExtSeq:    ext,
			SSRC:      pkt.SSRC,
			Marker:    pkt.Marker,
			Timestamp: pkt.Timestamp,
			Payload:   append([]byte(nil), pkt.Payload...),
		}
		if dup := known[ext]; dup != nil {
			rec.Duplicate = true
			res.Failures = append(res.Failures, model.NewFailure(trackID, model.StageRTP,
				rp.Offset, model.ErrRTPParse,
				"duplicate extended sequence "+uitoa(ext)+" first seen at offset "+itoa(dup.Offset)))
		} else {
			known[ext] = rec
		}
		res.Records = append(res.Records, *rec)
	}

	// Stable ordering: extended sequence first, then source offset so
	// duplicates of the same number keep arrival order.
	sortRecords(res.Records)
	if len(res.Records) > 0 {
		res.LastExtSeq = res.Records[len(res.Records)-1].ExtSeq
		res.Gaps = findGaps(res.Records, res.FirstExtSeq, res.LastExtSeq)
	}
	return res, nil
}

// unwrap maps a 16-bit sequence number onto the 64-bit line closest to prev.
// When no reference exists yet, seq is returned as-is. It uses the canonical
// signed-delta rule (RFC 3550 appendix-style unwrap), avoiding any signed
// overflow when comparing large uint64 candidates.
func unwrap(seq, prev uint64, haveRef bool) uint64 {
	if !haveRef {
		return seq
	}
	refCycle := prev &^ (seqMod - 1)
	candidate := refCycle | seq
	// Reinterpret the modular difference as a signed delta.
	delta := int64(candidate - prev)
	switch {
	case delta > int64(seqMod)/2:
		// Forward jump larger than half the space is really a backward wrap.
		return candidate - seqMod
	case delta < -int64(seqMod)/2:
		// Backward jump larger than half the space is really a forward wrap.
		return candidate + seqMod
	default:
		return candidate
	}
}

func findGaps(recs []Record, first, last uint64) []Gap {
	present := make(map[uint64]struct{}, len(recs))
	for _, r := range recs {
		present[r.ExtSeq] = struct{}{}
	}
	var gaps []Gap
	s := first
	for s <= last {
		if _, ok := present[s]; ok {
			s++
			continue
		}
		end := s
		for end < last {
			if _, ok := present[end+1]; ok {
				break
			}
			end++
		}
		gaps = append(gaps, Gap{From: s, To: end})
		if end == last {
			break
		}
		s = end + 1
	}
	return gaps
}

func sortRecords(recs []Record) {
	// Insertion sort keeps it simple; the input is bounded by MaxPacketsPerTrack.
	for i := 1; i < len(recs); i++ {
		for j := i; j > 0; j-- {
			a, b := recs[j-1], recs[j]
			if a.ExtSeq < b.ExtSeq || (a.ExtSeq == b.ExtSeq && a.Offset <= b.Offset) {
				break
			}
			recs[j-1], recs[j] = b, a
		}
	}
}

// ErrNotReconstructed is returned when a caller misuses a nil result.
var ErrNotReconstructed = errors.New("rtp: stream not reconstructed")

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
