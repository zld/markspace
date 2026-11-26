package ebml

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"

	"assetcheck/internal/media"
)

const (
	CodecPCM    = "A_PCM/INT/LIT"
	CodecCue    = "S_PWF_CUE1"
	CodecEffect = "E_PWF_EVENT1"
)

type CuePayload struct {
	CueID      string `json:"cue_id"`
	DurationMS uint32 `json:"duration_ms"`
	Text       string `json:"text"`
}

type EffectPayload struct {
	Name       string `json:"name"`
	DurationMS uint32 `json:"duration_ms"`
}

func EncodeCuePayload(cueID, text string, durationMS uint32) ([]byte, error) {
	if len(cueID) == 0 || len(cueID) > 255 {
		return nil, fmt.Errorf("cue id length must be 1..255")
	}
	out := make([]byte, 0, 1+len(cueID)+4+len(text))
	out = append(out, byte(len(cueID)))
	out = append(out, cueID...)
	var dur [4]byte
	binary.LittleEndian.PutUint32(dur[:], durationMS)
	out = append(out, dur[:]...)
	out = append(out, text...)
	return out, nil
}

func DecodeCuePayload(b []byte) (CuePayload, error) {
	if len(b) < 5 {
		return CuePayload{}, media.Corrupt("CUE_PAYLOAD_TRUNCATED", "cue payload lacks id and duration")
	}
	idLen := int(b[0])
	if idLen == 0 || len(b) < 1+idLen+4 {
		return CuePayload{}, media.Corrupt("CUE_PAYLOAD_TRUNCATED", "cue id or duration extends beyond block")
	}
	id := string(b[1 : 1+idLen])
	dur := binary.LittleEndian.Uint32(b[1+idLen : 5+idLen])
	text := string(b[5+idLen:])
	return CuePayload{CueID: id, DurationMS: dur, Text: text}, nil
}

func EncodeEffectPayload(name string, durationMS uint32) ([]byte, error) {
	if len(name) == 0 || len(name) > 255 {
		return nil, fmt.Errorf("effect name length must be 1..255")
	}
	out := make([]byte, 0, 1+len(name)+4)
	out = append(out, byte(len(name)))
	out = append(out, name...)
	var dur [4]byte
	binary.LittleEndian.PutUint32(dur[:], durationMS)
	out = append(out, dur[:]...)
	return out, nil
}

func DecodeEffectPayload(b []byte) (EffectPayload, error) {
	if len(b) < 5 {
		return EffectPayload{}, media.Corrupt("EFFECT_PAYLOAD_TRUNCATED", "effect payload lacks name and duration")
	}
	nameLen := int(b[0])
	if nameLen == 0 || len(b) < 1+nameLen+4 {
		return EffectPayload{}, media.Corrupt("EFFECT_PAYLOAD_TRUNCATED", "effect name or duration extends beyond block")
	}
	name := string(b[1 : 1+nameLen])
	dur := binary.LittleEndian.Uint32(b[1+nameLen : 5+nameLen])
	return EffectPayload{Name: name, DurationMS: dur}, nil
}

func locateClockError(err error, trackID, blockIndex int, offset int64, path string) error {
	if me, ok := media.AsError(err); ok {
		return me.WithTrack(trackID, blockIndex).WithElement(path, offset)
	}
	return media.Corrupt("CLOCK_CONVERSION_FAILED", "unable to convert block time").
		WithTrack(trackID, blockIndex).WithElement(path, offset).Wrap(err)
}

func (p *parser) findTrack(number uint64) *TrackInfo {
	for i := range p.doc.Tracks {
		if p.doc.Tracks[i].Number == number {
			return &p.doc.Tracks[i]
		}
	}
	return nil
}

func timecodeToRational(timecode int64, scaleNS uint64, loc media.ClockLocation) (media.Rational, error) {
	if scaleNS == 0 || scaleNS > math.MaxInt64 {
		return media.Zero, media.Corrupt("CLOCK_BAD_SCALE", "EBML timecode scale is zero or out of range").
			WithTrack(loc.TrackID, loc.BlockIndex).WithElement("", loc.Offset)
	}
	num := new(big.Int).Mul(big.NewInt(timecode), big.NewInt(int64(scaleNS)))
	if !num.IsInt64() {
		return media.Zero, media.Corrupt("CLOCK_OVERFLOW", "EBML timecode conversion overflows rational").
			WithTrack(loc.TrackID, loc.BlockIndex).WithElement("", loc.Offset).
			WithDetail(fmt.Sprintf("timecode=%d scale_ns=%d", timecode, scaleNS))
	}
	r, err := media.Rat(num.Int64(), int64(media.Nanosecond.D))
	if err != nil {
		return media.Zero, err
	}
	return r, nil
}

func (p *parser) parseSimpleBlock(end int64) error {
	blockOffset := p.r.Offset()
	tv, err := ReadVINT(p.r)
	if err != nil {
		return err
	}
	if tv.Unknown || tv.Value == 0 {
		return media.Corrupt("BLOCK_BAD_TRACK", "block track number must be a known non-zero VINT").
			WithElement(p.pathString(), tv.Offset)
	}
	trackID := int(tv.Value)

	var header [3]byte
	if err := p.r.ReadFull(header[:]); err != nil {
		return err
	}
	relative := int64(int16(binary.BigEndian.Uint16(header[0:2])))
	headerEnd := p.r.Offset()
	if end < 0 {
		return media.Corrupt("BLOCK_UNKNOWN_BOUNDARY", "cannot frame a block without an element or parent boundary").
			WithElement(p.pathString(), blockOffset)
	}
	frameSize := end - headerEnd
	if frameSize < 0 {
		return media.Corrupt("EBML_LENGTH_OUT_OF_BOUNDS", "block header exceeds element length").
			WithTrack(trackID, 0).WithElement(p.pathString(), blockOffset)
	}
	if frameSize > p.opts.MaxElementSize {
		return media.Corrupt("EBML_LENGTH_OUT_OF_BOUNDS", "block payload exceeds parser limit").
			WithTrack(trackID, 0).WithElement(p.pathString(), blockOffset)
	}
	payload := make([]byte, frameSize)
	blockIndex := p.blocks[tv.Value] + 1
	if err := p.r.ReadFull(payload); err != nil {
		if me, ok := media.AsError(err); ok {
			me.TrackID = trackID
			me.BlockIndex = blockIndex
		}
		return err
	}

	p.blocks[tv.Value] = blockIndex
	p.doc.BlockCount++
	loc := media.ClockLocation{TrackID: trackID, BlockIndex: blockIndex, Offset: blockOffset}
	startTC := p.clusterTC + relative
	start, err := timecodeToRational(startTC, p.doc.Metadata.TimecodeScale, loc)
	if err != nil {
		return err
	}

	track := p.findTrack(tv.Value)
	if track == nil {
		return media.Corrupt("BLOCK_UNKNOWN_TRACK", "block refers to undeclared track").
			WithTrack(trackID, blockIndex).WithElement(p.pathString(), blockOffset)
	}

	ev := media.TimeEvent{
		TrackID:    trackID,
		BlockIndex: blockIndex,
		Start:      start,
		Offset:     blockOffset,
		Payload:    payload,
	}

	switch track.Type {
	case TrackTypeAudio:
		if track.CodecID != CodecPCM {
			return media.Unsupported("CODEC_UNSUPPORTED", "audio codec is not supported by the offline verification kernel").
				WithTrack(trackID, blockIndex).WithElement(p.pathString(), blockOffset).
				WithDetail(track.CodecID)
		}
		ev.Kind = media.EventAudio
		rate := track.SamplingFrequency
		if rate <= 0 || track.Channels == 0 || track.BitsPerSample == 0 || track.BitsPerSample%8 != 0 {
			return media.Corrupt("AUDIO_CONFIG_INVALID", "PCM track lacks valid rate, channels, or bit depth").
				WithTrack(trackID, blockIndex).WithElement(p.pathString(), blockOffset)
		}
		bytesPerSecond := rate * float64(track.Channels) * float64(track.BitsPerSample) / 8
		duration, err := media.Rat(int64(len(payload)), int64(bytesPerSecond))
		if err != nil {
			return err
		}
		ev.End, err = media.Add(start, duration)
		if err != nil {
			return locateClockError(err, trackID, blockIndex, blockOffset, p.pathString())
		}

	case TrackTypeSubtitle:
		if track.CodecID != CodecCue {
			return media.Unsupported("CODEC_UNSUPPORTED", "subtitle codec is not supported").
				WithTrack(trackID, blockIndex).WithElement(p.pathString(), blockOffset).
				WithDetail(track.CodecID)
		}
		ev.Kind = media.EventSubtitle
		cue, err := DecodeCuePayload(payload)
		if err != nil {
			if me, ok := media.AsError(err); ok {
				me.TrackID = trackID
				me.BlockIndex = blockIndex
				me.ElementPath = p.pathString()
				me.Offset = blockOffset
			}
			return err
		}
		ev.CueID = cue.CueID
		ev.Text = cue.Text
		duration, err := media.Rat(int64(cue.DurationMS), 1000)
		if err != nil {
			return err
		}
		ev.End, err = media.Add(start, duration)
		if err != nil {
			return locateClockError(err, trackID, blockIndex, blockOffset, p.pathString())
		}

	case TrackTypeEffect:
		if track.CodecID != CodecEffect {
			return media.Unsupported("CODEC_UNSUPPORTED", "effect codec is not supported").
				WithTrack(trackID, blockIndex).WithElement(p.pathString(), blockOffset).
				WithDetail(track.CodecID)
		}
		ev.Kind = media.EventEffect
		fx, err := DecodeEffectPayload(payload)
		if err != nil {
			if me, ok := media.AsError(err); ok {
				me.TrackID = trackID
				me.BlockIndex = blockIndex
				me.ElementPath = p.pathString()
				me.Offset = blockOffset
			}
			return err
		}
		ev.EffectName = fx.Name
		duration, err := media.Rat(int64(fx.DurationMS), 1000)
		if err != nil {
			return err
		}
		ev.End, err = media.Add(start, duration)
		if err != nil {
			return locateClockError(err, trackID, blockIndex, blockOffset, p.pathString())
		}

	default:
		return media.Unsupported("TRACK_TYPE_UNSUPPORTED", "track type has no unified event adapter").
			WithTrack(trackID, blockIndex).WithElement(p.pathString(), blockOffset).
			WithDetail(fmt.Sprintf("type=%d", track.Type))
	}

	if p.opts.HandleEvent != nil {
		if err := p.opts.HandleEvent(ev); err != nil {
			return err
		}
	}
	return nil
}
