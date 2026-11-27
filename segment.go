package mediacore

import (
	"bytes"
	"io"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"
)

// ColorInfo is the resolved NCLX color triple plus the full-range flag.
type ColorInfo struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
	Present   bool
	SourceOff int64 // byte offset of the colr box; -1 when absent
}

// SegmentInfo is the kernel view of one fragmented MP4 segment: timing in
// nanoseconds, sample accounting, color metadata and payload fingerprint.
type SegmentInfo struct {
	TrackID    uint32
	Timescale  uint32
	BaseTime   Nano   // tfdt baseMediaDecodeTime converted to nanoseconds
	Duration   Nano   // sum of trun sample durations in nanoseconds
	Samples    uint32 // stsz/trun sample count
	Payload    int64  // mdat payload bytes
	Color      ColorInfo
	SHA256     [32]byte
	BoxOffsets map[string]int64 // first-seen byte offset per box type
}

// InspectSegment parses an fMP4 fragment from r. Only the boxes needed for
// timing, sample accounting and color metadata are deserialized. Bare
// fragments carry no mdhd, so expectedTS supplies the manifest timescale; an
// mdhd box, when present (init segment), takes precedence.
func InspectSegment(asset string, track uint32, expectedTS uint32, r io.ReadSeeker) (*SegmentInfo, error) {
	info := &SegmentInfo{TrackID: track, Timescale: expectedTS,
		BoxOffsets: map[string]int64{}, Color: ColorInfo{SourceOff: -1}}
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		bt := h.BoxInfo.Type.String()
		if _, seen := info.BoxOffsets[bt]; !seen {
			info.BoxOffsets[bt] = int64(h.BoxInfo.Offset)
		}
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMdhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			info.Timescale = box.(*mp4.Mdhd).Timescale
		case mp4.BoxTypeTfhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			info.TrackID = box.(*mp4.Tfhd).TrackID
		case mp4.BoxTypeTfdt():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			ns, nerr := ScaleChecked(int64(box.(*mp4.Tfdt).GetBaseMediaDecodeTime()), info.Timescale)
			if nerr != nil {
				return nil, ReStage(nerr, asset, track, StageSegment, int64(h.BoxInfo.Offset))
			}
			info.BaseTime = ns
		case mp4.BoxTypeTrun():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			trun := box.(*mp4.Trun)
			info.Samples += trun.SampleCount
			for _, e := range trun.Entries {
				ns, nerr := ScaleChecked(int64(e.SampleDuration), info.Timescale)
				if nerr != nil {
					return nil, ReStage(nerr, asset, track, StageSegment, int64(h.BoxInfo.Offset))
				}
				info.Duration += ns
			}
		case mp4.BoxTypeStts():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			stts := box.(*mp4.Stts)
			for _, e := range stts.Entries {
				ns, nerr := ScaleChecked(int64(e.SampleCount)*int64(e.SampleDelta), info.Timescale)
				if nerr != nil {
					return nil, ReStage(nerr, asset, track, StageSegment, int64(h.BoxInfo.Offset))
				}
				info.Duration += ns
				info.Samples += e.SampleCount
			}
		case mp4.BoxTypeStsz():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			stsz := box.(*mp4.Stsz)
			if stsz.SampleSize == 0 {
				info.Samples += stsz.SampleCount
			} else {
				info.Samples += stsz.SampleCount
			}
		case mp4.BoxTypeColr():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			c := box.(*mp4.Colr)
			if c.ColourType == [4]byte{'n', 'c', 'l', 'x'} {
				info.Color = ColorInfo{Primaries: c.ColourPrimaries, Transfer: c.TransferCharacteristics,
					Matrix: c.MatrixCoefficients, FullRange: c.FullRangeFlag, Present: true,
					SourceOff: int64(h.BoxInfo.Offset)}
			}
		case mp4.BoxTypeMdat():
			info.Payload += int64(h.BoxInfo.Size - h.BoxInfo.HeaderSize)
		}
		return h.Expand()
	})
	if err != nil {
		return nil, ReStage(err, asset, track, StageSegment, -1)
	}
	if info.Timescale == 0 {
		return nil, WrapMsg(ErrZeroTimescale, asset, track, StageSegment, -1,
			"no timescale available (manifest or mdhd)")
	}
	return info, nil
}

// RTPAnomaly describes a discontinuity discovered while walking RTP sequence
// numbers. Start/End are nanosecond positions derived from the packet index.
type RTPAnomaly struct {
	Kind        string // "gap" | "duplicate" | "reorder"
	StartNS     Nano
	EndNS       Nano
	GotSeq      uint16
	ExpectedSeq uint16
}

// InspectRTPStream deserializes a length-prefixed RTP packet capture (each
// record: 2-byte big-endian length followed by one RTP packet) and verifies
// per-packet header parsing plus monotonic sequence numbers via pion/rtp.
func InspectRTPStream(asset string, r io.Reader, packetStepNS Nano) ([]*rtp.Packet, []RTPAnomaly, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, Wrap(err, asset, 0, StageSegment, -1)
	}
	var pkts []*rtp.Packet
	var anomalies []RTPAnomaly
	var expected uint16
	have := false
	for off := 0; off+2 <= len(raw); {
		n := int(uint16(raw[off])<<8 | uint16(raw[off+1]))
		off += 2
		if n == 0 || off+n > len(raw) {
			return nil, nil, WrapMsg(ErrIllegalField, asset, 0, StageSegment, int64(off),
				"truncated RTP capture record")
		}
		p := &rtp.Packet{}
		if err := p.Unmarshal(raw[off : off+n]); err != nil {
			return nil, nil, WrapMsg(err, asset, 0, StageSegment, int64(off), "bad RTP header")
		}
		idx := Nano(len(pkts))
		if have {
			switch {
			case p.SequenceNumber == expected-1 && len(pkts) > 0:
				anomalies = append(anomalies, RTPAnomaly{Kind: "duplicate",
					StartNS: idx * packetStepNS, EndNS: (idx + 1) * packetStepNS,
					GotSeq: p.SequenceNumber, ExpectedSeq: expected})
			case p.SequenceNumber != expected:
				kind := "gap"
				if delta16(p.SequenceNumber, expected) > 0x8000 {
					kind = "reorder"
				}
				anomalies = append(anomalies, RTPAnomaly{Kind: kind,
					StartNS: idx * packetStepNS, EndNS: (idx + 1) * packetStepNS,
					GotSeq: p.SequenceNumber, ExpectedSeq: expected})
			}
		}
		expected = p.SequenceNumber + 1
		have = true
		pkts = append(pkts, p)
		off += n
	}
	return pkts, anomalies, nil
}

// delta16 returns the signed forward distance from want to got in uint16 space.
func delta16(got, want uint16) uint16 { return got - want }

// FingerprintPayload returns the SHA-256 of the mdat-equivalent payload bytes.
// It is the idempotency key: byte-identical re-coverage merges for free.
func FingerprintPayload(b []byte) [32]byte { return sha256Sum(bytes.NewReader(b)) }
