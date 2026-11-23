package cmaf

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"

	mp4 "github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"mediapack/internal/clock"
)

// Sample describes one entry of a trun box.
type Sample struct {
	Duration uint32
	Size     uint32
	Flags    uint32
}

// Fragment is the parsed form of one CMAF/fMP4 fragment file.
type Fragment struct {
	TrackID        uint32
	SequenceNumber uint32
	HasTfdt        bool
	BaseDecodeTime uint64
	Samples        []Sample
	Duration       int64 // sum of sample durations in track ticks, 0 if underivable
	Keyframe       bool
	MoofOffset     int64
	MoofSize       int64
	Hash           [32]byte
}

// nonSyncFlag is sample_is_non_sync_sample in trun sample flags.
const nonSyncFlag = 0x00010000

// trun optional-field flags.
const (
	trunDataOffset       = 0x000001
	trunFirstSampleFlags = 0x000004
	trunSampleDuration   = 0x000100
	trunSampleSize       = 0x000200
	trunSampleFlags      = 0x000400
)

// ParseFragment parses one fragment file. Structural problems are reported
// as parse-category errors carrying the byte offset.
func ParseFragment(data []byte) (*Fragment, error) {
	headers, err := ScanBoxes(data)
	if err != nil {
		return nil, err
	}
	f := &Fragment{Hash: sha256.Sum256(data)}
	var moof *BoxHeader
	for i := range headers {
		if headers[i].Type == "moof" {
			moof = &headers[i]
			break
		}
	}
	if moof == nil {
		return nil, &clock.Error{Cat: clock.CatParse, Msg: "fragment has no moof box"}
	}
	f.MoofOffset, f.MoofSize = moof.Offset, moof.Size
	if err := f.parseMoof(data[moof.Offset : moof.Offset+moof.Size]); err != nil {
		return nil, err
	}
	if f.TrackID == 0 {
		return nil, &clock.Error{Cat: clock.CatParse, Offset: moof.Offset,
			Msg: "missing tfhd track id"}
	}
	if len(f.Samples) == 0 {
		return nil, &clock.Error{Cat: clock.CatParse, Offset: moof.Offset,
			Msg: "missing trun samples"}
	}
	return f, nil
}

func (f *Fragment) parseMoof(moof []byte) error {
	var mfhdSeen bool
	var defaultDuration uint32
	_, err := mp4.ReadBoxStructure(bytes.NewReader(moof), func(h *mp4.ReadHandle) (interface{}, error) {
		depth := len(h.Path)
		switch {
		case depth == 2 && h.BoxInfo.Type == mp4.BoxTypeMfhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			f.SequenceNumber = box.(*mp4.Mfhd).SequenceNumber
			mfhdSeen = true
		case depth == 3 && h.BoxInfo.Type == mp4.BoxTypeTfhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			tfhd := box.(*mp4.Tfhd)
			f.TrackID = tfhd.TrackID
			defaultDuration = tfhd.DefaultSampleDuration
		case depth == 3 && h.BoxInfo.Type == mp4.BoxTypeTfdt():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			f.HasTfdt = true
			f.BaseDecodeTime = box.(*mp4.Tfdt).GetBaseMediaDecodeTime()
		case depth == 3 && h.BoxInfo.Type == mp4.BoxTypeTrun():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			f.addTrun(box.(*mp4.Trun), defaultDuration)
		}
		if h.BoxInfo.Type == mp4.BoxTypeMoof() || h.BoxInfo.Type == mp4.BoxTypeTraf() {
			return h.Expand()
		}
		return nil, nil
	})
	if err != nil {
		return &clock.Error{Cat: clock.CatParse, Offset: f.MoofOffset,
			Msg: "moof parse failure", Err: err}
	}
	if !mfhdSeen {
		return &clock.Error{Cat: clock.CatParse, Offset: f.MoofOffset,
			Msg: "missing mfhd box"}
	}
	return nil
}

func (f *Fragment) addTrun(trun *mp4.Trun, defaultDuration uint32) {
	flags := trun.GetFlags()
	for i := range trun.Entries {
		e := &trun.Entries[i]
		s := Sample{Duration: defaultDuration, Flags: 0x02000000}
		if flags&trunSampleDuration != 0 {
			s.Duration = e.SampleDuration
		}
		if flags&trunSampleSize != 0 {
			s.Size = e.SampleSize
		}
		if flags&trunSampleFlags != 0 {
			s.Flags = e.SampleFlags
		}
		if len(f.Samples) == 0 && flags&trunFirstSampleFlags != 0 {
			s.Flags = trun.FirstSampleFlags
		}
		f.Samples = append(f.Samples, s)
		f.Duration += int64(s.Duration)
		if len(f.Samples) == 1 {
			f.Keyframe = s.Flags&nonSyncFlag == 0
		}
	}
}

// SourceMarker is a provenance marker parsed from a synthetic RTP stream.
type SourceMarker struct {
	SSRC      uint32
	Sequence  uint16
	Timestamp uint32
}

// ParseSourceMarkers decodes length-prefixed RTP packets: a 2-byte
// big-endian length followed by one RTP packet each.
func ParseSourceMarkers(data []byte) ([]SourceMarker, error) {
	var out []SourceMarker
	pos := 0
	for pos < len(data) {
		if len(data)-pos < 2 {
			return nil, &clock.Error{Cat: clock.CatParse, Offset: int64(pos),
				Msg: "truncated marker length"}
		}
		n := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if n == 0 || len(data)-pos < n {
			return nil, &clock.Error{Cat: clock.CatParse, Offset: int64(pos),
				Msg: "truncated marker packet"}
		}
		var p rtp.Packet
		if err := p.Unmarshal(data[pos : pos+n]); err != nil {
			return nil, &clock.Error{Cat: clock.CatParse, Offset: int64(pos),
				Msg: "invalid RTP marker", Err: err}
		}
		out = append(out, SourceMarker{
			SSRC:      p.SSRC,
			Sequence:  p.SequenceNumber,
			Timestamp: p.Timestamp,
		})
		pos += n
	}
	return out, nil
}
