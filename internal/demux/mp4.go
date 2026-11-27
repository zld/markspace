// Package demux turns container fragments into the shared model.Track shape
// while preserving compressed sample bytes byte-for-byte.
package demux

import (
	"bytes"
	"errors"
	"io"

	"github.com/abema/go-mp4"

	"mediarc/internal/model"
)

// MP4Demuxer reads fragmented MP4 (DASH) segments.
type MP4Demuxer struct {
	file string
}

// NewMP4Demuxer binds a logical file name used for offset-attributed errors.
func NewMP4Demuxer(file string) *MP4Demuxer { return &MP4Demuxer{file: file} }

type mp4State struct {
	track    model.Track
	baseDts  uint64
	dts      uint64
	moofOff  uint64
	dataOff  int64
	haveData bool
}

// Demux extracts tracks from one fragmented MP4 buffer. A parse failure is
// returned as a model.MediaError carrying the offending byte offset.
func (d *MP4Demuxer) Demux(data []byte) ([]model.Track, error) {
	r := bytes.NewReader(data)
	meta, err := readTrackMeta(d.file, r)
	if err != nil {
		return nil, err
	}

	states := map[uint32]*mp4State{}
	var active *mp4State
	var curMoofOffset uint64
	get := func(id uint32) *mp4State {
		s, ok := states[id]
		if !ok {
			m := meta[id]
			s = &mp4State{track: model.Track{
				ID: id, Codec: m.codec, Timescale: m.timescale,
				NotMappable: m.codec == model.CodecUnknown,
			}}
			states[id] = s
		}
		return s
	}

	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoof():
			curMoofOffset = h.BoxInfo.Offset
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		case mp4.BoxTypeTraf():
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		case mp4.BoxTypeTfhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, d.offsetErr(h.BoxInfo.Offset, err)
			}
			tfhd := box.(*mp4.Tfhd)
			active = get(tfhd.TrackID)
			active.moofOff = curMoofOffset
		case mp4.BoxTypeTfdt():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, d.offsetErr(h.BoxInfo.Offset, err)
			}
			tfdt := box.(*mp4.Tfdt)
			if active == nil {
				return nil, d.offsetErr(h.BoxInfo.Offset, errors.New("tfdt without preceding tfhd"))
			}
			if tfdt.Version == 1 {
				active.baseDts = tfdt.BaseMediaDecodeTimeV1
			} else {
				active.baseDts = uint64(tfdt.BaseMediaDecodeTimeV0)
			}
			active.dts = active.baseDts
		case mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, d.offsetErr(h.BoxInfo.Offset, err)
			}
			if active == nil {
				return nil, d.offsetErr(h.BoxInfo.Offset, errors.New("trun without preceding tfhd"))
			}
			trun := box.(*mp4.Trun)
			if trun.CheckFlag(0x000001) {
				active.dataOff = int64(active.moofOff) + int64(trun.DataOffset)
				active.haveData = true
			}
			if !active.haveData {
				return nil, d.offsetErr(h.BoxInfo.Offset, errors.New("trun without data offset"))
			}
			if active.dataOff < 0 || active.dataOff >= int64(len(data)) {
				return nil, model.AtOffset(model.KindSegmentCorrupt, d.file, active.dataOff, "",
					"trun data offset out of range")
			}
			payload := data[active.dataOff:]
			off := 0
			for _, e := range trun.Entries {
				size := int(e.SampleSize)
				if size == 0 {
					continue
				}
				if off+size > len(payload) {
					return nil, model.AtOffset(model.KindSegmentCorrupt, d.file, int64(h.BoxInfo.Offset), "",
						"trun sample exceeds mdat payload")
				}
				payloadCopy := make([]byte, size)
				copy(payloadCopy, payload[off:off+size])
				active.track.Samples = append(active.track.Samples, model.Sample{
					Dts:      active.dts,
					Duration: e.SampleDuration,
					Payload:  payloadCopy,
					// bit 0x10000 is sample_is_non_sync_sample; absence means sync.
					Sync: e.SampleFlags&0x10000 == 0,
				})
				active.dts += uint64(e.SampleDuration)
				off += size
			}
		}
		return nil, nil
	}

	if _, err := mp4.ReadBoxStructure(bytes.NewReader(data), handler); err != nil {
		if me, ok := err.(*model.MediaError); ok {
			return nil, me
		}
		return nil, model.AtOffset(model.KindSegmentCorrupt, d.file, 0, "", err.Error())
	}

	out := make([]model.Track, 0, len(states))
	for _, s := range states {
		out = append(out, s.track)
	}
	if len(out) == 0 {
		return nil, model.AtOffset(model.KindSegmentCorrupt, d.file, 0, "", "no tracks found in fragment")
	}
	return out, nil
}

type trackMeta struct {
	timescale uint32
	codec     model.Codec
}

func readTrackMeta(fileName string, r io.ReadSeeker) (map[uint32]trackMeta, error) {
	meta := map[uint32]trackMeta{}
	var curID uint32
	var handlerType string
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
			mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd():
			return h.Expand()
		case mp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			curID = box.(*mp4.Tkhd).TrackID
			handlerType = ""
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mdhd := box.(*mp4.Mdhd)
			// hdlr may be visited after mdhd; the recorded handlerType (or a
			// pending empty value) is resolved again when hdlr arrives.
			meta[curID] = trackMeta{timescale: mdhd.Timescale, codec: codecForHandler(handlerType)}
		case mp4.BoxTypeHdlr():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			handlerType = string(bytes.TrimRight(box.(*mp4.Hdlr).HandlerType[:], "\x00"))
			if m, ok := meta[curID]; ok {
				m.codec = codecForHandler(handlerType)
				meta[curID] = m
			}
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, model.AtOffset(model.KindSegmentCorrupt, fileName, 0, "", err.Error())
	}
	return meta, nil
}

func codecForHandler(handler string) model.Codec {
	switch handler {
	case "vide":
		return model.CodecAVC1
	case "soun":
		return model.CodecMP4A
	default:
		return model.CodecUnknown
	}
}

func (d *MP4Demuxer) offsetErr(off uint64, err error) *model.MediaError {
	return model.AtOffset(model.KindSegmentCorrupt, d.file, int64(off), "", err.Error())
}
