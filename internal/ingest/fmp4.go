package ingest

import (
	"audionorm/internal/media"
	"encoding/binary"
	"errors"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// Fragment describes one fMP4 sample's location and coded extent.
type Fragment struct {
	Offset      int64  // byte offset of the sample payload in the file
	Size        uint32 // payload size in bytes
	BaseTime    uint64 // tfdt baseMediaDecodeTime, in movie timescale
	Duration    uint32 // trun sample duration (0 if index incomplete)
	IndexOffset int64  // byte offset of the owning trun box, for errors
}

// ParseFMP4 reads a fragmented MP4 containing one AAC-LC track and
// returns every fragment plus the movie timescale. A truncated or
// incomplete index produces INDEX_INCOMPLETE records on the error log;
// samples whose extent cannot be located stay pending and are never
// guessed. The seeker is consumed sequentially (streaming-friendly).
func ParseFMP4(r io.ReadSeeker) ([]Fragment, uint32, *media.ErrorLog, error) {
	errs := media.NewErrorLog()
	var timescale uint32
	var out []Fragment

	for {
		bi, err := mp4.ReadBoxInfo(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
				"box header unreadable: "+err.Error(), 0, 0))
			break
		}
		switch bi.Type.String() {
		case "moov":
			ts, err := readTimescale(r, bi)
			if err != nil {
				errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
					err.Error(), 0, int64(bi.Offset)))
			}
			timescale = ts
			if err := skipBox(r, bi); err != nil {
				return out, timescale, errs, err
			}
		case "moof":
			frags, err := readMoof(r, bi, errs)
			if err != nil {
				return out, timescale, errs, err
			}
			out = append(out, frags...)
		default:
			if err := skipBox(r, bi); err != nil {
				return out, timescale, errs, err
			}
		}
	}
	if timescale == 0 {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"movie timescale missing", 0, 0))
	}
	return out, timescale, errs, nil
}

func skipBox(r io.Seeker, bi *mp4.BoxInfo) error {
	_, err := r.Seek(int64(bi.Offset+bi.Size), io.SeekStart)
	return err
}

func readTimescale(r io.ReadSeeker, bi *mp4.BoxInfo) (uint32, error) {
	boxes, err := mp4.ExtractBoxWithPayload(r, bi,
		mp4.BoxPath{mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
			mp4.BoxTypeMdhd()})
	if err != nil {
		return 0, err
	}
	if len(boxes) == 0 {
		return 0, errors.New("mdhd box not found in moov")
	}
	return boxes[0].Payload.(*mp4.Mdhd).Timescale, nil
}

// readMoof parses one moof/mdat pair. The reader must be positioned at
// the moof header; on return it is positioned past the mdat payload.
func readMoof(r io.ReadSeeker, bi *mp4.BoxInfo, errs *media.ErrorLog) ([]Fragment, error) {
	tfdts, err := mp4.ExtractBoxWithPayload(r, bi,
		mp4.BoxPath{mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()})
	if err != nil {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"tfdt unreadable: "+err.Error(), 0, int64(bi.Offset)))
		return nil, skipBox(r, bi)
	}
	if len(tfdts) == 0 {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"moof missing tfdt", 0, int64(bi.Offset)))
		return nil, skipBox(r, bi)
	}
	tfdt := tfdts[0].Payload.(*mp4.Tfdt)
	base := tfdt.GetBaseMediaDecodeTime()

	// Locate trun boxes without fully unmarshalling them, so a truncated
	// trun still yields the entries that fit inside its declared size.
	trunInfos, err := mp4.ExtractBox(r, bi,
		mp4.BoxPath{mp4.BoxTypeTraf(), mp4.BoxTypeTrun()})
	if err != nil || len(trunInfos) == 0 {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"moof missing readable trun", 0, int64(bi.Offset)))
		return nil, skipBox(r, bi)
	}
	trun, trunFlags, truncated := parseTrun(r, trunInfos[0], errs)

	// Extraction moved the seeker; reposition just past the moof box.
	if _, err := r.Seek(int64(bi.Offset+bi.Size), io.SeekStart); err != nil {
		return nil, err
	}
	// The mdat header follows the moof immediately.
	mbi, err := mp4.ReadBoxInfo(r)
	if err != nil {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"mdat header unreadable: "+err.Error(), 0, int64(bi.Offset+bi.Size)))
		return nil, nil
	}
	if mbi.Type.String() != "mdat" {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"moof not followed by mdat", 0, int64(mbi.Offset)))
		return nil, skipBox(r, mbi)
	}
	dataStart := int64(mbi.Offset) + int64(mbi.HeaderSize) + int64(trun.DataOffset)

	var frags []Fragment
	cursor := dataStart
	running := base
	for i := uint32(0); i < trun.SampleCount; i++ {
		if int(i) >= len(trun.Entries) {
			// Truncated index: extent unknown -> HOLD, no guessing.
			errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
				"trun entry missing", uint64(i), int64(trunInfos[0].Offset)))
			break
		}
		e := trun.Entries[i]
		size := e.SampleSize
		if trunFlags&0x000200 == 0 {
			errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
				"trun lacks sample-size field", uint64(i), int64(trunInfos[0].Offset)))
			break
		}
		dur := e.SampleDuration
		if trunFlags&0x000100 == 0 {
			// No per-sample duration: interval cannot be located.
			errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
				"trun lacks sample-duration field", uint64(i), int64(trunInfos[0].Offset)))
			dur = 0
		}
		if size > media.MaxPacketBytes {
			errs.Add(media.NewPendingError(media.ErrPacketTooLarge,
				"fmp4 sample exceeds 1 MiB", uint64(i), cursor))
		}
		frags = append(frags, Fragment{
			Offset: cursor, Size: size, BaseTime: running,
			Duration: dur, IndexOffset: int64(trunInfos[0].Offset),
		})
		cursor += int64(size)
		running += uint64(dur)
	}
	_ = truncated
	if err := skipBox(r, mbi); err != nil {
		return frags, err
	}
	return frags, nil
}

// parseTrun reads a trun box at the lowest level so a truncated tail
// (declared SampleCount larger than the entries actually present) still
// returns the complete leading entries. The boolean result reports
// whether truncation was detected.
func parseTrun(r io.ReadSeeker, info *mp4.BoxInfo, errs *media.ErrorLog) (*mp4.Trun, uint32, bool) {
	if _, err := info.SeekToPayload(r); err != nil {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"trun seek failed: "+err.Error(), 0, int64(info.Offset)))
		return &mp4.Trun{}, 0, true
	}
	// Bound every read to the physical box so a truncated trun cannot
	// pull garbage bytes from the boxes that follow it.
	lr := io.LimitReader(r, int64(info.Size-info.HeaderSize))
	header := make([]byte, 4)
	if _, err := io.ReadFull(lr, header); err != nil {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"trun fullbox header unreadable", 0, int64(info.Offset)))
		return &mp4.Trun{}, 0, true
	}
	flags := uint32(header[1])<<16 | uint32(header[2])<<8 | uint32(header[3])
	countBuf := make([]byte, 4)
	if _, err := io.ReadFull(lr, countBuf); err != nil {
		errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"trun sample count unreadable", 0, int64(info.Offset)))
		return &mp4.Trun{FullBox: mp4.FullBox{Flags: [3]byte{header[1], header[2], header[3]}}}, flags, true
	}
	count := binary.BigEndian.Uint32(countBuf)
	trun := &mp4.Trun{
		FullBox:     mp4.FullBox{Version: header[0], Flags: [3]byte{header[1], header[2], header[3]}},
		SampleCount: count,
	}
	if flags&0x000001 != 0 {
		if _, err := io.CopyN(io.Discard, lr, 4); err != nil {
			return trun, flags, true
		}
	}
	if flags&0x000004 != 0 {
		if _, err := io.CopyN(io.Discard, lr, 4); err != nil {
			return trun, flags, true
		}
	}
	entrySize := 0
	for _, bit := range []uint32{0x000100, 0x000200, 0x000400, 0x000800} {
		if flags&bit != 0 {
			entrySize += 4
		}
	}
	for i := uint32(0); i < count; i++ {
		raw := make([]byte, entrySize)
		if _, err := io.ReadFull(lr, raw); err != nil {
			errs.Add(media.NewPendingError(media.ErrIndexIncomplete,
				"trun entries truncated", uint64(i), int64(info.Offset)))
			return trun, flags, true
		}
		var e mp4.TrunEntry
		pos := 0
		if flags&0x000100 != 0 {
			e.SampleDuration = binary.BigEndian.Uint32(raw[pos:])
			pos += 4
		}
		if flags&0x000200 != 0 {
			e.SampleSize = binary.BigEndian.Uint32(raw[pos:])
			pos += 4
		}
		if flags&0x000400 != 0 {
			e.SampleFlags = binary.BigEndian.Uint32(raw[pos:])
			pos += 4
		}
		if flags&0x000800 != 0 {
			e.SampleCompositionTimeOffsetV0 = binary.BigEndian.Uint32(raw[pos:])
		}
		trun.Entries = append(trun.Entries, e)
	}
	return trun, flags, false
}

// FMP4Ingester turns parsed fragments into timeline packets, mapping the
// movie timescale onto 48 kHz with remainder retention.
type FMP4Ingester struct {
	conv     *media.RateConverter
	res      *Result
	codec    Codec
	channels uint16
}

// NewFMP4Ingester builds an ingester for an AAC-LC fMP4 stream.
func NewFMP4Ingester(timescale uint32, channels uint16) *FMP4Ingester {
	if channels != 1 && channels != 2 {
		channels = 0
	}
	return &FMP4Ingester{
		conv:     media.NewRateConverter(uint64(timescale)),
		res:      NewResult(),
		codec:    CodecAAC,
		channels: channels,
	}
}

// AddFragment places one fragment. Data is read lazily by the caller via
// the returned byte range; here only timing is resolved.
func (in *FMP4Ingester) AddFragment(f Fragment, payload []byte) {
	seq := uint64(len(in.res.Packets))
	if in.channels == 0 {
		in.res.Errors.Add(media.NewPendingError(media.ErrUnsupportedLayout,
			"only mono or stereo AAC-LC is accepted", seq, f.Offset))
		return
	}
	if f.Duration == 0 {
		in.res.Errors.Add(media.NewPendingError(media.ErrIndexIncomplete,
			"sample duration unknown, interval held", seq, f.IndexOffset))
		return
	}
	n, _ := in.conv.Convert(uint64(f.Duration))
	end := in.conv.OutputOffset()
	start := end - n
	if uint32(len(payload)) != f.Size {
		in.res.Errors.Add(media.NewPendingError(media.ErrCorruptPacket,
			"sample payload size disagrees with index", seq, f.Offset))
		return
	}
	in.res.Packets = append(in.res.Packets, Packet{
		Codec: in.codec, ExtSeq: seq, Payload: append([]byte(nil), payload...),
		When: media.Range{Start: start, End: start + n},
		ByteOffset: f.Offset, SampleRate: uint32(in.conv.SourceRate),
		Channels: in.channels,
	})
}

// Result returns the accumulated ingest result.
func (in *FMP4Ingester) Result() *Result { return in.res }
