package inspect

import (
	"bytes"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"

	"archivecheck/internal/model"
)

// Init is the decoded initialization segment metadata.
type Init struct {
	// File is the init segment name.
	File string `json:"file"`
	// Tracks maps track ID to its metadata.
	Tracks map[uint32]InitTrack `json:"tracks"`
	// Timescales are the distinct track timescales.
	Timescales []uint32 `json:"timescales"`
}

// InitTrack is one track's initialization metadata.
type InitTrack struct {
	TrackID            uint32 `json:"track_id"`
	Timescale          uint32 `json:"timescale"`
	Handler            string `json:"handler"`
	DefaultSampleDur   uint32 `json:"default_sample_duration"`
	DefaultSampleSize  uint32 `json:"default_sample_size"`
	DefaultSampleFlags uint32 `json:"default_sample_flags"`
}

// TrackByID returns metadata for a track.
func (i *Init) TrackByID(id uint32) (InitTrack, bool) {
	t, ok := i.Tracks[id]
	return t, ok
}

// ParseInit decodes an initialization segment (ftyp+moov) with mp4ff and
// extracts per-track timescales and default sample values.
func ParseInit(name string, data []byte) (*Init, error) {
	f, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		return nil, model.NewFault(model.CodeMP4Structure, model.SeverityError,
			src(name, 0), "cannot decode initialization segment: %v", err).WithCause(err)
	}
	if f.Moov == nil {
		return nil, model.NewFault(model.CodeMP4Structure, model.SeverityError,
			src(name, 0), "initialization segment has no moov box")
	}
	out := &Init{File: name, Tracks: map[uint32]InitTrack{}}
	for _, trak := range f.Moov.Traks {
		if trak == nil || trak.Mdia == nil || trak.Mdia.Mdhd == nil || trak.Tkhd == nil {
			return nil, model.NewFault(model.CodeMP4Structure, model.SeverityError,
				src(name, 0), "track is missing tkhd/mdia/mdhd")
		}
		id := trak.Tkhd.TrackID
		t := InitTrack{
			TrackID:   id,
			Timescale: trak.Mdia.Mdhd.Timescale,
			Handler:   trak.Mdia.Hdlr.HandlerType,
		}
		if t.Timescale == 0 {
			return nil, model.NewFault(model.CodeMP4TrackTimebase, model.SeverityError,
				src(name, 0), "track %d declares a zero timescale", id)
		}
		if f.Moov.Mvex != nil {
			for _, trex := range f.Moov.Mvex.Trexs {
				if trex.TrackID == id {
					t.DefaultSampleDur = trex.DefaultSampleDuration
					t.DefaultSampleSize = trex.DefaultSampleSize
					t.DefaultSampleFlags = trex.DefaultSampleFlags
				}
			}
		}
		out.Tracks[id] = t
	}
	if len(out.Tracks) == 0 {
		return nil, model.NewFault(model.CodeMP4Structure, model.SeverityError,
			src(name, 0), "initialization segment declares no tracks")
	}
	out.Timescales = timescalesOf(out.Tracks)
	return out, nil
}

func timescalesOf(tracks map[uint32]InitTrack) []uint32 {
	seen := map[uint32]bool{}
	var out []uint32
	for _, t := range tracks {
		if !seen[t.Timescale] {
			seen[t.Timescale] = true
			out = append(out, t.Timescale)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sampleFlagsSync mirrors ISO/IEC 14496-12 8.8.3.1 sync detection.
func sampleFlagsSync(flags uint32) bool {
	sf := mp4.DecodeSampleFlags(flags)
	return !sf.SampleIsNonSync && sf.SampleDependsOn == 2
}

// decodeMoof decodes a single moof box payload using mp4ff.
func decodeMoof(name string, data []byte, box Box) (*mp4.MoofBox, *model.Fault) {
	slice := data[box.Offset : box.Offset+box.Size]
	decoded, err := mp4.DecodeBox(uint64(box.Offset), bytes.NewReader(slice))
	if err != nil {
		return nil, model.NewFault(model.CodeMP4Structure, model.SeverityError,
			src(name, box.Offset), "cannot decode moof: %v", err).WithCause(err).
			WithBytes(box.Offset, box.Offset+box.Size)
	}
	moof, ok := decoded.(*mp4.MoofBox)
	if !ok {
		return nil, model.NewFault(model.CodeMP4Structure, model.SeverityError,
			src(name, box.Offset), "expected moof, decoded %T", decoded)
	}
	return moof, nil
}

func trexFor(init *Init, id uint32) *mp4.TrexBox {
	if init == nil {
		return nil
	}
	if t, ok := init.Tracks[id]; ok {
		trex := mp4.CreateTrex(id)
		trex.DefaultSampleDuration = t.DefaultSampleDur
		trex.DefaultSampleSize = t.DefaultSampleSize
		trex.DefaultSampleFlags = t.DefaultSampleFlags
		return trex
	}
	return nil
}
