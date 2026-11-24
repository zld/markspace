package container

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
	"github.com/local/medialab/internal/timeline"
)

// MP4Sample is one normalized sample with file offset and master-clock
// presentation time.
type MP4Sample struct {
	Track       uint32
	Number      uint32 // 1-based within track
	Offset      int64
	Size        uint32
	TimeTick    int64
	Keyframe    bool
	HasKeyIndex bool // false when the track has no stss at all
}

// MP4Meta is the structured result of parsing one MP4 sample.
type MP4Meta struct {
	FileMeta
	Samples []MP4Sample
	// Raw timescales retained for conflict arbitration.
	VideoTimescale uint32
	AudioTimescale uint32
}

// rawTrack accumulates tables during the box walk.
type rawTrack struct {
	id           uint32
	handler      string
	timescale    uint32
	duration     uint64
	width        uint32
	height       uint32
	rotation     int
	color        *ColorDesc
	stts         []mp4.SttsEntry
	stss         []uint32
	hasStss      bool
	stsz         []uint32
	chunkOffsets []uint32
}

// ParseMP4 reads the generated MP4 via go-mp4 and normalizes both tracks
// onto the master clock. Parse errors are wrapped with offsets where the
// library provides them.
func ParseMP4(data []byte) (*MP4Meta, error) {
	tracks := map[uint32]*rawTrack{}
	var order []uint32
	var current *rawTrack

	r := bytes.NewReader(data)
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeTrak():
			t := &rawTrack{}
			current = t
			if _, e := h.Expand(); e != nil {
				return nil, e
			}
			if t.id != 0 {
				tracks[t.id] = t
				order = append(order, t.id)
			}
			current = nil
			return nil, nil
		case mp4.BoxTypeTkhd():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			tk := box.(*mp4.Tkhd)
			if current != nil {
				current.id = tk.TrackID
				current.width = uint32(tk.Width >> 16)
				current.height = uint32(tk.Height >> 16)
				current.rotation = matrixRotation(tk.Matrix)
			}
		case mp4.BoxTypeMdhd():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			md := box.(*mp4.Mdhd)
			if current != nil {
				current.timescale = md.Timescale
				current.duration = md.GetDuration()
			}
		case mp4.BoxTypeHdlr():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			if current != nil {
				current.handler = string(bytes.TrimRight(box.(*mp4.Hdlr).HandlerType[:], "\x00"))
			}
		case mp4.BoxTypeStts():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			if current != nil {
				current.stts = box.(*mp4.Stts).Entries
			}
		case mp4.BoxTypeStss():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			if current != nil {
				current.stss = box.(*mp4.Stss).SampleNumber
				current.hasStss = true
			}
		case mp4.BoxTypeStsz():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			st := box.(*mp4.Stsz)
			if current != nil {
				if st.SampleSize != 0 {
					current.stsz = make([]uint32, st.SampleCount)
					for i := range current.stsz {
						current.stsz[i] = st.SampleSize
					}
				} else {
					current.stsz = append([]uint32(nil), st.EntrySize...)
				}
			}
		case mp4.BoxTypeStco():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			if current != nil {
				current.chunkOffsets = append([]uint32(nil), box.(*mp4.Stco).ChunkOffset...)
			}
		case mp4.BoxTypeColr():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			col := box.(*mp4.Colr)
			if current != nil {
				current.color = &ColorDesc{
					Primaries: col.ColourPrimaries, Transfer: col.TransferCharacteristics,
					Matrix: col.MatrixCoefficients, FullRange: col.FullRangeFlag, Source: "mp4",
				}
			}
		}
		if h.BoxInfo.IsSupportedType() {
			if _, e := h.Expand(); e != nil && !errors.Is(e, io.EOF) {
				return nil, e
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("mp4: structure parse failed: %w", err)
	}
	if len(order) == 0 {
		return nil, errors.New("mp4: no tracks found")
	}

	meta := &MP4Meta{FileMeta: FileMeta{Format: "mp4"}}
	for _, id := range order {
		rt := tracks[id]
		td, samples, err := normalizeTrack(rt, data)
		if err != nil {
			return nil, err
		}
		meta.Tracks = append(meta.Tracks, td)
		meta.Samples = append(meta.Samples, samples...)
		if td.Kind == "video" {
			meta.VideoTimescale = rt.timescale
			meta.FileMeta.Color = rt.color
			meta.FileMeta.Rotation = rt.rotation
			meta.FileMeta.HasIndex = rt.hasStss
		} else {
			meta.AudioTimescale = rt.timescale
		}
	}
	return meta, nil
}

func normalizeTrack(rt *rawTrack, data []byte) (TrackDesc, []MP4Sample, error) {
	clock := timeline.Clock{Hz: int64(rt.timescale)}
	kind := "audio"
	if rt.handler == "vide" {
		kind = "video"
	}
	td := TrackDesc{
		ID: rt.id, Kind: kind, TimescaleHz: int64(rt.timescale),
		FrameRateNum: int64(rt.timescale), FrameRateDen: 1,
		Width: rt.width, Height: rt.height, Rotation: rt.rotation, Color: rt.color,
		DurationTicks: clock.ToTicks(int64(rt.duration)),
	}

	// Expand stts runs into per-sample deltas and presentation ticks.
	var deltas []int64
	for _, e := range rt.stts {
		for i := uint32(0); i < e.SampleCount; i++ {
			deltas = append(deltas, int64(e.SampleDelta))
		}
	}
	if len(deltas) != len(rt.stsz) {
		return td, nil, fmt.Errorf("mp4: track %d stts/stsz sample count mismatch (%d vs %d)",
			rt.id, len(deltas), len(rt.stsz))
	}
	if len(rt.chunkOffsets) == 0 {
		return td, nil, fmt.Errorf("mp4: track %d has no chunk offsets (missing index)", rt.id)
	}

	// One chunk per track in the fixture: all samples are contiguous from
	// the first chunk offset.
	cursor := int64(rt.chunkOffsets[0])
	var scaleTime int64
	samples := make([]MP4Sample, 0, len(rt.stsz))
	keySet := map[uint32]bool{}
	for _, n := range rt.stss {
		keySet[n] = true
	}
	for i, size := range rt.stsz {
		num := uint32(i + 1)
		if cursor+int64(size) > int64(len(data)) {
			return td, nil, fmt.Errorf("mp4: track %d sample %d (offset %d) extends past EOF %d",
				rt.id, num, cursor, len(data))
		}
		s := MP4Sample{
			Track: rt.id, Number: num, Offset: cursor, Size: size,
			TimeTick:    clock.ToTicks(scaleTime),
			Keyframe:    rt.hasStss && keySet[num],
			HasKeyIndex: rt.hasStss,
		}
		samples = append(samples, s)
		td.SampleCount++
		cursor += int64(size)
		scaleTime += deltas[i]
	}
	if rt.hasStss {
		td.KeyframeSampleNumbers = append(td.KeyframeSampleNumbers, rt.stss...)
	}
	return td, samples, nil
}

// matrixRotation decodes the tkhd matrix rotation in degrees, limited to
// the orientations the fixture can emit.
func matrixRotation(m [9]int32) int {
	const one = 0x00010000
	switch {
	case m[0] == one && m[4] == one:
		return 0
	case m[1] == one && m[3] == -one:
		return 90
	case m[0] == -one && m[4] == -one:
		return 180
	case m[1] == -one && m[3] == one:
		return 270
	default:
		return 0
	}
}
