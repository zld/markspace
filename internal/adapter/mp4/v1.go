// Package mp4 is the v1 fragmented-MP4 adapter. It parses a deliberately
// small, registered subset of ISOBMFF: styp, an optional "meds" descriptor
// carrying timescale/kind, and moof/traf/{tfhd,tfdt,trun}+mdat fragments.
// No codecs are decoded; frame payloads are passed through opaquely.
package mp4

import (
	"bytes"
	"fmt"
	"os"

	go_mp4 "github.com/abema/go-mp4"

	"mediad/internal/adapter"
	"mediad/internal/model"
)

// Version is pinned in checkpoints and provenance records.
const Version = "mp4-v1"

// meds is the fixture-defined descriptor box:
//
//	version(1) flags(3) timescale(4) kind(1: 0=video,1=audio)
const medsType = "meds"

type parser struct{}

func init() { adapter.Register(parser{}) }

func (parser) Format() string  { return "mp4" }
func (parser) Version() string { return Version }

type parseState struct {
	timescale model.TimeScale
	kind      model.TrackKind
	baseTick  uint64
	trackID   uint32
	trun      *go_mp4.Trun
	moofOff   int64 // byte offset of the pending moof; -1 when none
	mdat      [][]byte
	frames    []model.Frame
	loc       model.SourceLoc
}

func (parser) Parse(in adapter.MediaInput) (*model.MediaTrack, error) {
	loc := model.SourceLoc{
		Manifest: in.Manifest, Representation: in.Representation,
		Entry: in.Entry, Segment: in.Path, Offset: -1, Frame: -1,
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return nil, model.WrapError(model.KindCorruptBox, loc, err)
	}
	st := &parseState{loc: loc, moofOff: -1}
	r := bytes.NewReader(data)
	_, err = go_mp4.ReadBoxStructure(r, func(h *go_mp4.ReadHandle) (interface{}, error) {
		return st.handle(h)
	})
	if err != nil {
		if se, ok := model.AsSource(err); ok {
			return nil, se
		}
		return nil, model.NewError(model.KindCorruptBox, loc, err.Error())
	}
	if !st.timescale.Valid() {
		return nil, model.NewError(model.KindZeroTimescale, loc,
			"segment has no meds timescale descriptor")
	}
	if len(st.frames) == 0 {
		return nil, model.NewError(model.KindTruncatedFrame, loc,
			"segment produced no frames")
	}
	return &model.MediaTrack{
		ID: in.Representation, Kind: st.kind, TimeScale: st.timescale,
		Frames: st.frames,
		Source: model.SourceRef{
			Manifest: in.Manifest, Representation: in.Representation,
			Entry: in.Entry, File: in.Path,
		},
	}, nil
}

func (st *parseState) handle(h *go_mp4.ReadHandle) (interface{}, error) {
	boxType := h.BoxInfo.Type.String()
	switch boxType {
	case medsType:
		return st.readMeds(h)
	case "moof":
		st.trun = nil
		st.moofOff = int64(h.BoxInfo.Offset)
		if _, err := h.Expand(); err != nil {
			return nil, model.WrapError(model.KindCorruptBox, st.locAt(h.BoxInfo.Offset), err)
		}
		return nil, nil
	case "tfhd":
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, model.WrapError(model.KindCorruptBox, st.locAt(h.BoxInfo.Offset), err)
		}
		st.trackID = box.(*go_mp4.Tfhd).TrackID
		return nil, nil
	case "tfdt":
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, model.WrapError(model.KindCorruptBox, st.locAt(h.BoxInfo.Offset), err)
		}
		t := box.(*go_mp4.Tfdt)
		if t.GetVersion() == 1 {
			st.baseTick = t.BaseMediaDecodeTimeV1
		} else {
			st.baseTick = uint64(t.BaseMediaDecodeTimeV0)
		}
		return nil, nil
	case "trun":
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, model.WrapError(model.KindCorruptBox, st.locAt(h.BoxInfo.Offset), err)
		}
		st.trun = box.(*go_mp4.Trun)
		return nil, nil
	case "mdat":
		var buf bytes.Buffer
		if _, err := h.ReadData(&buf); err != nil {
			return nil, model.WrapError(model.KindCorruptBox, st.locAt(h.BoxInfo.Offset), err)
		}
		st.mdat = append(st.mdat, buf.Bytes())
		return nil, st.materializeMoof(h.BoxInfo.Offset)
	default:
		// Unknown containers are descended into; unknown leaf boxes skipped.
		if h.BoxInfo.IsSupportedType() {
			return h.Expand()
		}
		return nil, nil
	}
}

func (st *parseState) readMeds(h *go_mp4.ReadHandle) (interface{}, error) {
	var buf bytes.Buffer
	if _, err := h.ReadData(&buf); err != nil {
		return nil, model.WrapError(model.KindCorruptBox, st.locAt(h.BoxInfo.Offset), err)
	}
	b := buf.Bytes()
	if len(b) < 9 {
		return nil, model.NewError(model.KindCorruptBox, st.locAt(h.BoxInfo.Offset),
			fmt.Sprintf("meds payload too short: %d bytes", len(b)))
	}
	ts := uint32(b[4])<<24 | uint32(b[5])<<16 | uint32(b[6])<<8 | uint32(b[7])
	if ts == 0 {
		return nil, model.NewError(model.KindZeroTimescale, st.locAt(h.BoxInfo.Offset),
			"meds declares zero timescale")
	}
	st.timescale = model.TimeScale(ts)
	if b[8] == 1 {
		st.kind = model.TrackAudio
	} else {
		st.kind = model.TrackVideo
	}
	return nil, nil
}

func (st *parseState) materializeMoof(mdatOff uint64) error {
	if st.trun == nil || len(st.mdat) == 0 {
		off := uint64(0)
		if st.moofOff >= 0 {
			off = uint64(st.moofOff)
		}
		return model.NewError(model.KindCorruptBox, st.locAt(off),
			"mdat without preceding moof/trun")
	}
	payload := st.mdat[len(st.mdat)-1]
	trun := st.trun
	if uint64(trun.SampleCount) > uint64(len(trun.Entries)) {
		return model.NewError(model.KindCorruptBox, st.locAt(uint64(st.moofOff)),
			fmt.Sprintf("trun sample count %d exceeds entries %d", trun.SampleCount, len(trun.Entries)))
	}
	// Frames carry segment-relative PTS (sample tick minus the moof base
	// media decode time); the core timeline re-bases them with the manifest
	// segment anchor, mirroring how RTP captures start their clock at zero.
	tick := st.baseTick
	offset := 0
	for i := 0; i < int(trun.SampleCount); i++ {
		e := trun.Entries[i]
		size := int(e.SampleSize)
		if size < 0 || size > len(payload)-offset {
			return model.NewError(model.KindTruncatedFrame,
				st.locFrame(mdatOff, i),
				fmt.Sprintf("frame %d size %d exceeds remaining mdat %d", i, size, len(payload)-offset))
		}
		if tick < st.baseTick {
			return model.NewError(model.KindFieldOutOfRange, st.locFrame(mdatOff, i),
				"sample tick precedes tfdt base media decode time")
		}
		pts, err := model.ScaleRound(int64(tick-st.baseTick), st.timescale)
		if err != nil {
			return model.WrapError(model.KindFieldOutOfRange, st.locFrame(mdatOff, i), err)
		}
		dur, err := model.ScaleRound(int64(e.SampleDuration), st.timescale)
		if err != nil {
			return model.WrapError(model.KindFieldOutOfRange, st.locFrame(mdatOff, i), err)
		}
		frame := model.Frame{
			Kind: st.kind, PTS: pts, Dur: dur,
			// ISO sample_depends_on is the 2-bit field at bits 24-25;
			// value 2 marks a non-sample, i.e. a random-access keyframe.
			KeyFrame: e.SampleFlags&0x03000000 == 0x02000000,
			Payload:  append([]byte(nil), payload[offset:offset+size]...),
		}
		st.frames = append(st.frames, frame)
		offset += size
		tick += uint64(e.SampleDuration)
	}
	st.moofOff = -1
	return nil
}

func (st *parseState) locAt(off uint64) model.SourceLoc {
	l := st.loc
	l.Offset = int64(off)
	return l
}

func (st *parseState) locFrame(off uint64, frame int) model.SourceLoc {
	l := st.locAt(off)
	l.Frame = frame
	return l
}
