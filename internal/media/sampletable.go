package media

import (
	"encoding/binary"
	"math"

	"github.com/pion/rtp"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/timebase"
)

const sampleTableMagic = "AUD1"

type SampleSpec struct {
	KeyFrame  bool
	PTS       int64
	Duration  uint32
	DependsOn int32
	Sequence  uint16
	Timestamp uint32
	Payload   []byte
}

func EncodeSampleTable(specs []SampleSpec) []byte {
	var out []byte
	out = append(out, sampleTableMagic...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(specs)))
	for i, s := range specs {
		rec := make([]byte, 0, 64+len(s.Payload))
		rec = binary.BigEndian.AppendUint32(rec, uint32(i))
		var flags byte
		if s.KeyFrame {
			flags |= 1
		}
		rec = append(rec, flags)
		rec = binary.BigEndian.AppendUint64(rec, uint64(s.PTS))
		rec = binary.BigEndian.AppendUint32(rec, s.Duration)
		dep := uint32(math.MaxUint32)
		if s.DependsOn >= 0 {
			dep = uint32(s.DependsOn)
		}
		rec = binary.BigEndian.AppendUint32(rec, dep)

		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: s.Sequence,
				Timestamp:      s.Timestamp,
				SSRC:           0x41554431,
			},
			Payload: s.Payload,
		}
		pktBytes, _ := pkt.Marshal()
		rec = binary.BigEndian.AppendUint16(rec, uint16(len(pktBytes)))
		rec = append(rec, pktBytes...)
		out = append(out, rec...)
	}
	return out
}

func u16(b []byte, p int) uint16 { return binary.BigEndian.Uint16(b[p : p+2]) }
func u32(b []byte, p int) uint32 { return binary.BigEndian.Uint32(b[p : p+4]) }
func u64(b []byte, p int) uint64 { return binary.BigEndian.Uint64(b[p : p+8]) }

// ParseSampleTable decodes records. base is the file offset of data[0].
func ParseSampleTable(data []byte, base int64, tb timebase.Timebase, trackID uint32) ([]Sample, []*auditerrors.Error) {
	var errs []*auditerrors.Error
	if len(data) < 8 || string(data[:4]) != sampleTableMagic {
		e := auditerrors.New(auditerrors.CodeUnknownStructure, base, "bad sample-table magic")
		errs = append(errs, e.WithTrack(trackID).WithBytes(base, base+int64(len(data))))
		return nil, errs
	}
	count := u32(data, 4)
	pos := 8
	samples := make([]Sample, 0, minInt(int(count), 64))

	for i := uint32(0); i < count; i++ {
		recordStart := base + int64(pos)
		need := func(n int) bool {
			if pos+n <= len(data) {
				return true
			}
			eof := base + int64(len(data))
			e := auditerrors.New(auditerrors.CodeTruncated, eof, "sample %d is truncated", i).
				WithTrack(trackID).WithBytes(recordStart, eof)
			errs = append(errs, e)
			return false
		}
		if !need(21) {
			return samples, errs
		}

		idx := u32(data, pos)
		flags := data[pos+4]
		ptsOffset := base + int64(pos+5)
		pts := int64(u64(data, pos+5))
		duration := u32(data, pos+13)
		depRaw := u32(data, pos+17)
		depends := int32(-1)
		if depRaw != math.MaxUint32 {
			depends = int32(depRaw)
		}
		pos += 21

		if pts < 0 {
			e := auditerrors.New(auditerrors.CodeInvalidTimestamp, ptsOffset, "sample %d has negative PTS %d", idx, pts).
				WithTrack(trackID).
				WithExpectedActual("PTS >= 0", "negative signed value")
			errs = append(errs, e)
		}

		if !need(2) {
			return samples, errs
		}
		pktLen := int(u16(data, pos))
		pos += 2
		if !need(pktLen) {
			return samples, errs
		}
		pktStart := base + int64(pos)
		pktBytes := data[pos : pos+pktLen]
		pos += pktLen

		var pkt rtp.Packet
		if err := pkt.Unmarshal(pktBytes); err != nil {
			e := auditerrors.New(auditerrors.CodeUnknownStructure, pktStart, "invalid RTP packet: %v", err).
				WithTrack(trackID).WithBytes(pktStart, pktStart+int64(pktLen))
			errs = append(errs, e)
		}

		s := Sample{
			Index:       idx,
			KeyFrame:    flags&1 == 1,
			PTS:         pts,
			Duration:    duration,
			DependsOn:   depends,
			RecordStart: recordStart,
			RecordEnd:   base + int64(pos),
			RTP: PacketInfo{
				Sequence:        pkt.Header.SequenceNumber,
				Timestamp:       pkt.Header.Timestamp,
				PacketOffset:    pktStart,
				SequenceOffset:  pktStart + 2,
				TimestampOffset: pktStart + 4,
			},
		}
		samples = append(samples, s)

		if len(samples) >= 2 {
			prev := samples[len(samples)-2]
			if delta := int32(s.RTP.Timestamp - prev.RTP.Timestamp); delta < 0 {
				e := auditerrors.New(
					auditerrors.CodeTimestampWrap,
					s.RTP.TimestampOffset,
					"RTP timestamp moves backward by %d ticks after boundary %d->%d",
					-delta, prev.RTP.Timestamp, s.RTP.Timestamp,
				).WithTrack(trackID).
					WithExpectedActual("non-negative modular delta", "negative delta")
				if r, err := timebase.ConvertRange(tb, max64(0, s.PTS), max64(0, s.PTS)+int64(s.Duration)); err == nil {
					e.WithInterval(r)
				}
				e.Bytes = &auditerrors.ByteRange{Start: s.RecordStart, End: s.RecordEnd}
				errs = append(errs, e)
			}
			if seqDelta := int16(s.RTP.Sequence - prev.RTP.Sequence); seqDelta != 1 {
				e := auditerrors.New(
					auditerrors.CodeSequenceGap,
					s.RTP.SequenceOffset,
					"RTP sequence gap %d->%d (modular delta %d)",
					prev.RTP.Sequence, s.RTP.Sequence, seqDelta,
				).WithTrack(trackID)
				e.Bytes = &auditerrors.ByteRange{Start: s.RecordStart, End: s.RecordEnd}
				errs = append(errs, e)
			}
		}
	}
	return samples, errs
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
