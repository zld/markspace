package bitstream

import (
	"fmt"

	"mediq/internal/timeline"
)

// Limits caps the resources a single scan may consume. The 8 GiB input
// cap mirrors the single-file processing ceiling; unit and RTP packet
// quotas stop pathological inputs from exhausting memory or CPU.
type Limits struct {
	MaxInputBytes int64
	MaxUnits      int
	MaxRTPPackets int
}

// DefaultLimits returns the production resource quotas.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes: 8 << 30, // 8 GiB
		MaxUnits:      1_000_000,
		MaxRTPPackets: 10_000_000,
	}
}

// ScanResult is the output of scanning one elementary stream. Only
// complete units are present; a truncated tail is reported separately
// and never becomes a unit. Corrupt but fully delimited units are kept
// with Corrupt=true so downstream stages can quarantine them.
type ScanResult struct {
	Codec         Codec
	Units         []Unit
	Truncated     bool
	TruncOffset   int64
	CompleteBytes int64
	RTPPackets    int
	Errors        []*timeline.Error
}

// LastCompleteUnit returns the final fully parseable unit, if any.
func (r *ScanResult) LastCompleteUnit() (Unit, bool) {
	if len(r.Units) == 0 {
		return Unit{}, false
	}
	return r.Units[len(r.Units)-1], true
}

// Keyframes returns complete, non-corrupt random access units in order.
func (r *ScanResult) Keyframes() []Unit {
	var out []Unit
	for _, u := range r.Units {
		if u.Keyframe && !u.Corrupt {
			out = append(out, u)
		}
	}
	return out
}

// RAPUnits returns every random access unit in order, including
// quarantined (corrupt) ones. Planning uses these to build spans so a
// damaged key frame becomes an isolated segment instead of vanishing.
func (r *ScanResult) RAPUnits() []Unit {
	var out []Unit
	for _, u := range r.Units {
		if u.Keyframe {
			out = append(out, u)
		}
	}
	return out
}

// Scan parses a raw elementary stream. Recoverable damage (truncation,
// corruption) is reported on the result; only quota violations return
// an error, because those mean the scan cannot continue safely.
func Scan(codec Codec, data []byte, lim Limits) (*ScanResult, error) {
	if int64(len(data)) > lim.MaxInputBytes {
		return nil, timeline.New(timeline.StageScan, timeline.KindQuotaExceeded,
			int64(len(data)), "reject_input",
			fmt.Sprintf("input %d bytes exceeds limit %d", len(data), lim.MaxInputBytes))
	}
	switch codec {
	case CodecH264, CodecH265:
		return scanAnnexB(codec, data, lim)
	case CodecAV1:
		return scanAV1(data, lim)
	default:
		return nil, fmt.Errorf("bitstream: unknown codec %d", codec)
	}
}

// truncated records a truncated tail and stops the scan at offset off.
func (r *ScanResult) truncated(off int64, lim Limits) (*ScanResult, error) {
	r.Truncated = true
	r.TruncOffset = int64(off)
	r.CompleteBytes = int64(off)
	r.Errors = append(r.Errors, timeline.New(timeline.StageScan, timeline.KindTruncated,
		int64(off), "discard_incomplete_tail",
		"incomplete final unit excluded from commit"))
	return r, r.checkQuota(lim)
}

func (r *ScanResult) checkQuota(lim Limits) error {
	if len(r.Units) > lim.MaxUnits {
		return timeline.New(timeline.StageScan, timeline.KindQuotaExceeded,
			r.CompleteBytes, "abort_scan",
			fmt.Sprintf("unit count %d exceeds quota %d", len(r.Units), lim.MaxUnits))
	}
	if r.RTPPackets > lim.MaxRTPPackets {
		return timeline.New(timeline.StageScan, timeline.KindQuotaExceeded,
			r.CompleteBytes, "abort_scan",
			fmt.Sprintf("rtp packet count %d exceeds quota %d", r.RTPPackets, lim.MaxRTPPackets))
	}
	return nil
}

// scanAnnexB parses H.264/H.265 Annex B byte stream. NAL unit bodies
// are emulation-prevention escaped on disk (see EscapeEPB), so start
// code scanning never sees false boundaries; bodies are unescaped
// before meta and CRC validation.
func scanAnnexB(codec Codec, data []byte, lim Limits) (*ScanResult, error) {
	res := &ScanResult{Codec: codec}
	hdrLen := 1
	if codec == CodecH265 {
		hdrLen = 2
	}
	starts := findStartCodes(data)
	last := len(starts) - 1
	for i, off := range starts {
		end := len(data)
		if i < last {
			end = starts[i+1]
		}
		span := data[off:end]
		u := Unit{Codec: codec, Offset: int64(off), Length: int64(len(span))}
		inner := UnescapeEPB(span[4+hdrLen:]) // meta + payload, EPB removed
		if len(inner) < MetaLen {
			return res.truncated(int64(off), lim) // incomplete meta header
		}
		if codec == CodecH265 {
			u.Type = h265Type(span[4])
		} else {
			u.Type = h264Type(span[4])
		}
		u.Keyframe = isRAP(codec, u.Type)
		m, ok := decodeMeta(inner)
		if !ok {
			u.Corrupt = true
			res.Errors = append(res.Errors, timeline.New(timeline.StageScan,
				timeline.KindCorruptUnit, int64(off), "quarantine_unit", "bad meta magic"))
		} else {
			u.Seq, u.PTS = m.seq, m.pts
			want := MetaLen + int(m.declaredLen)
			if want > len(inner) {
				if i == last {
					return res.truncated(int64(off), lim) // declared payload runs past EOF
				}
				u.Corrupt = true
				res.Errors = append(res.Errors, timeline.New(timeline.StageScan,
					timeline.KindCorruptUnit, int64(off), "quarantine_unit",
					"declared unit length exceeds span"))
			} else {
				payload := inner[MetaLen:want]
				if want != len(inner) || CRC32(payload) != m.crc {
					u.Corrupt = true
					res.Errors = append(res.Errors, timeline.New(timeline.StageScan,
						timeline.KindCorruptUnit, int64(off), "quarantine_unit",
						"length mismatch or crc32 failure"))
				}
				res.RTPPackets += rtpPacketCount(codec, payload, 1200)
			}
		}
		res.Units = append(res.Units, u)
		if err := res.checkQuota(lim); err != nil {
			return nil, err
		}
	}
	res.CompleteBytes = int64(len(data))
	return res, nil
}

// scanAV1 parses an AV1 low-overhead bitstream (OBUs with obu_size).
func scanAV1(data []byte, lim Limits) (*ScanResult, error) {
	res := &ScanResult{Codec: CodecAV1}
	pos := 0
	for pos < len(data) {
		off := pos
		hdr := data[pos]
		if hdr&0x80 != 0 { // forbidden bit
			res.CompleteBytes = int64(off)
			res.Errors = append(res.Errors, timeline.New(timeline.StageScan,
				timeline.KindCorruptUnit, int64(off), "stop_scan", "forbidden OBU header bit"))
			return res, res.checkQuota(lim)
		}
		if hdr&0x02 == 0 { // obu_has_size_flag is bit 1; synthetic streams always set it
			res.CompleteBytes = int64(off)
			res.Errors = append(res.Errors, timeline.New(timeline.StageScan,
				timeline.KindCorruptUnit, int64(off), "stop_scan", "OBU size field missing"))
			return res, res.checkQuota(lim)
		}
		typ := (hdr >> 3) & 0x1f
		size, n, ok := readLEB128(data[pos+1:])
		if !ok {
			return res.truncated(int64(off), lim)
		}
		payStart, payEnd := pos+1+n, pos+1+n+int(size)
		if payEnd > len(data) {
			return res.truncated(int64(off), lim)
		}
		payload := data[payStart:payEnd]
		u := Unit{Codec: CodecAV1, Type: typ, Offset: int64(off), Length: int64(payEnd - off)}
		if len(payload) < MetaLen {
			u.Corrupt = true
			res.Errors = append(res.Errors, timeline.New(timeline.StageScan,
				timeline.KindCorruptUnit, int64(off), "quarantine_unit", "OBU shorter than meta"))
		} else if m, mok := decodeMeta(payload); !mok {
			u.Corrupt = true
			res.Errors = append(res.Errors, timeline.New(timeline.StageScan,
				timeline.KindCorruptUnit, int64(off), "quarantine_unit", "bad meta magic"))
		} else {
			body := payload[MetaLen:]
			u.Seq, u.PTS = m.seq, m.pts
			if int(m.declaredLen) != len(body) || CRC32(body) != m.crc {
				u.Corrupt = true
				res.Errors = append(res.Errors, timeline.New(timeline.StageScan,
					timeline.KindCorruptUnit, int64(off), "quarantine_unit",
					"length mismatch or crc32 failure"))
			}
			// Synthetic convention: frame OBU (type 6) whose first body
			// byte is 1 is a key frame; 0 marks an inter frame.
			u.Keyframe = typ == 6 && len(body) > 0 && body[0] == 1
			res.RTPPackets += rtpPacketCount(CodecAV1, payload, 1200)
		}
		res.Units = append(res.Units, u)
		if err := res.checkQuota(lim); err != nil {
			return nil, err
		}
		pos = payEnd
	}
	res.CompleteBytes = int64(pos)
	return res, nil
}

// findStartCodes returns the start offsets (including the leading zero
// of a 4-byte code) of every Annex B start code in data.
func findStartCodes(data []byte) []int {
	var out []int
	i := 0
	for i+2 < len(data) {
		if data[i] != 0 || data[i+1] != 0 || data[i+2] != 1 {
			i++
			continue
		}
		off := i
		if i > 0 && data[i-1] == 0 {
			off = i - 1 // 00 00 00 01
		}
		out = append(out, off)
		i += 3
	}
	return out
}

// readLEB128 decodes an unsigned LEB128 value. ok=false means the
// buffer ends before the terminating byte.
func readLEB128(b []byte) (value uint64, n int, ok bool) {
	for shift := uint(0); shift < 64; shift += 7 {
		if n >= len(b) {
			return 0, n, false
		}
		c := b[n]
		n++
		value |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return value, n, true
		}
	}
	return 0, n, false
}
