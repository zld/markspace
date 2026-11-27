package ingest

import (
	"io"

	"github.com/abema/go-mp4"

	"mediad/internal/model"
)

// aacSampleRates maps the MPEG samplingFrequencyIndex (also carried in AU/ADTS
// headers) to Hz. Indexes 13-15 are reserved.
var aacSampleRates = [16]uint32{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350, 0, 0, 0,
}

// AUFrame is one AAC access unit located inside an RTP payload.
type AUFrame struct {
	Size       int
	Index      byte
	Offset     int // offset relative to payload start
	SampleRate uint32
}

// parseAAC parses the RFC 3640 AU headers (13-bit size + 3-bit index per AU),
// validates every length against the actual payload, and computes the total
// packet duration from the codec sample rate (1024 samples per AAC frame).
func parseAAC(spec TrackSpec, pkt *AudioPacket) error {
	const stage = "ingest"
	pl := pkt.Payload
	if len(pl) < 2 {
		return model.ErrAACHeader.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset, "missing AU-headers-length field")
	}
	headerBits := int(uint16(pl[0])<<8 | uint16(pl[1]))
	if headerBits == 0 || headerBits%16 != 0 {
		return model.ErrAACHeader.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset, "AU-headers-length must be a positive multiple of 16 bits")
	}
	headerBytes := headerBits / 8
	if 2+headerBytes > len(pl) {
		return model.ErrAACHeader.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset, "AU header section extends past payload")
	}
	rate := spec.AACSampleRate
	if rate == 0 {
		rate = aacSampleRates[4] // 44100 default for header-less fixtures
	}
	frameNS, err := model.ToNano(1024, rate)
	if err != nil {
		return err
	}
	dataOff := 2 + headerBytes
	cursor := dataOff
	for off := 2; off+1 < dataOff; off += 2 {
		word := uint16(pl[off])<<8 | uint16(pl[off+1])
		size := int(word >> 3)
		index := byte(word & 0x07)
		if size == 0 {
			return model.ErrAACHeader.At(spec.ID, uint32(pkt.Sequence), stage,
				pkt.Offset+int64(off), "zero-length AAC access unit")
		}
		if cursor+size > len(pl) {
			return model.ErrAACHeader.At(spec.ID, uint32(pkt.Sequence), stage,
				pkt.Offset+int64(off), "AU size exceeds remaining payload")
		}
		cursor += size
		_ = AUFrame{Size: size, Index: index, Offset: cursor - size, SampleRate: rate}
	}
	if cursor != len(pl) {
		return model.ErrAACHeader.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset+int64(cursor), "AU frame sizes do not cover payload exactly")
	}
	frames := headerBytes / 2
	total, err := model.AddNano(0, frameNS*model.Nano(frames))
	if err != nil {
		return model.ErrTimeOverflow.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset, "aac duration overflow")
	}
	ticks, err := model.RescaleNano(total, spec.ClockRate)
	if err != nil {
		return err
	}
	pkt.DurationNS = total
	pkt.DurationTC = ticks
	return nil
}

// MP4TimeBase is the lightweight timing information read from an MP4 track:
// native timescale, per-sample deltas and per-sample byte sizes.
type MP4TimeBase struct {
	Timescale     uint32
	DurationTicks uint64
	SampleDeltas  []uint32
	SampleSizes   []uint32
}

// ReadMP4TimeBase walks the (single-track, fixture-generated) MP4 boxes with
// go-mp4 and extracts the mdhd/stts/stsz timing information. Missing or
// inconsistent boxes produce a located ERR_MP4_TIME_BASE.
func ReadMP4TimeBase(r io.ReadSeeker) (*MP4TimeBase, error) {
	tb := &MP4TimeBase{}
	var sawMdhd, sawStts, sawStsz bool
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mdhd := box.(*mp4.Mdhd)
			if mdhd.Timescale == 0 {
				return nil, model.ErrMP4TimeBase.At("", 0, "ingest",
					int64(h.BoxInfo.Offset), "mdhd timescale is zero")
			}
			tb.Timescale = mdhd.Timescale
			tb.DurationTicks = mdhd.GetDuration()
			sawMdhd = true
		case mp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			stts := box.(*mp4.Stts)
			for _, e := range stts.Entries {
				for i := uint32(0); i < e.SampleCount; i++ {
					tb.SampleDeltas = append(tb.SampleDeltas, e.SampleDelta)
				}
			}
			sawStts = true
		case mp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			stsz := box.(*mp4.Stsz)
			if stsz.SampleSize != 0 {
				tb.SampleSizes = make([]uint32, stsz.SampleCount)
				for i := range tb.SampleSizes {
					tb.SampleSizes[i] = stsz.SampleSize
				}
			} else {
				tb.SampleSizes = append(tb.SampleSizes, stsz.EntrySize...)
			}
			sawStsz = true
		}
		return h.Expand()
	})
	if err != nil {
		if me := model.AsError(err); me.Code == model.ErrMP4TimeBase {
			return nil, err
		}
		return nil, model.ErrMP4TimeBase.At("", 0, "ingest", 0, err.Error())
	}
	if !sawMdhd || !sawStts {
		return nil, model.ErrMP4TimeBase.At("", 0, "ingest", 0,
			"mp4 missing mdhd or stts timing boxes")
	}
	if sawStsz && len(tb.SampleSizes) != len(tb.SampleDeltas) {
		return nil, model.ErrMP4TimeBase.At("", 0, "ingest", 0,
			"stsz sample count does not match stts sample count")
	}
	return tb, nil
}

// SampleNanos converts one MP4 sample delta to integer nanoseconds.
func (tb *MP4TimeBase) SampleNanos(delta uint32) (model.Nano, error) {
	return model.ToNano(int64(delta), tb.Timescale)
}
