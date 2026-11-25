// Package tracks builds the track dependency graph and validates codec and
// presentation metadata against local consistency rules.
package tracks

import (
	"mediacheck/internal/ebml"
	"mediacheck/internal/report"
)

// Channel layout IDs are small deterministic identifiers encoded in the
// generator's ChannelLayout metadata element.
const (
	LayoutUnknown = 0
	LayoutMono    = 1
	LayoutStereo  = 2
	Layout51      = 6
	Layout71      = 8
)

// LayoutChannels maps a layout identifier to its required channel count.
var LayoutChannels = map[uint64]int{
	LayoutMono:   1,
	LayoutStereo: 2,
	Layout51:     6,
	Layout71:     8,
}

// ColorMetadata retains raw offsets for every present colour field.
type ColorMetadata struct {
	Element         *ebml.Element
	Matrix          uint64
	MatrixOffset    int
	Transfer        uint64
	TransferOffset  int
	Primaries       uint64
	PrimariesOffset int
}

// Track is the metadata projection with offsets back into the input bytes.
type Track struct {
	Entry         *ebml.Element
	Number        uint64
	NumberOffset  int
	UID           uint64
	Type          uint64
	CodecID       string
	DependsOn     []uint64
	DependencyOff []int

	Channels       uint64
	ChannelsOffset int
	Layout         uint64
	LayoutOffset   int
	SampleRate     float64
	SampleRateOff  int
	OutputRate     float64
	OutputRateOff  int
	BitDepth       uint64

	PixelWidth  uint64
	PixelHeight uint64
	Color       *ColorMetadata
}

// Graph contains tracks keyed by track number.
type Graph struct {
	Tracks map[uint64]*Track
	Order  []uint64
}

// BuildGraph extracts all TrackEntry metadata and raw value offsets.
func BuildGraph(data []byte, doc *ebml.Document) (*Graph, *report.Error) {
	r := ebml.NewReader(data, doc.Limits)
	g := &Graph{Tracks: make(map[uint64]*Track)}
	for _, entry := range doc.TrackEntries {
		t := &Track{Entry: entry}
		if el := ebml.FindFirst(entry, ebml.IDTrackNumber); el != nil {
			v, err := r.ReadUint(el)
			if err != nil {
				return nil, err
			}
			t.Number = v
			t.NumberOffset = el.Offset
		}
		if el := ebml.FindFirst(entry, ebml.IDTrackUID); el != nil {
			v, err := r.ReadUint(el)
			if err != nil {
				return nil, err
			}
			t.UID = v
		}
		if el := ebml.FindFirst(entry, ebml.IDTrackType); el != nil {
			v, err := r.ReadUint(el)
			if err != nil {
				return nil, err
			}
			t.Type = v
		}
		if el := ebml.FindFirst(entry, ebml.IDCodecID); el != nil {
			s, err := r.ReadString(el)
			if err != nil {
				return nil, err
			}
			t.CodecID = s
		}
		for _, el := range ebml.FindChildren(entry, ebml.IDTrackDependsOn) {
			v, err := r.ReadUint(el)
			if err != nil {
				return nil, err
			}
			t.DependsOn = append(t.DependsOn, v)
			t.DependencyOff = append(t.DependencyOff, el.Offset)
		}
		if audio := ebml.FindFirst(entry, ebml.IDAudio); audio != nil {
			if err := extractAudio(r, t, audio); err != nil {
				return nil, err
			}
		}
		if video := ebml.FindFirst(entry, ebml.IDVideo); video != nil {
			if err := extractVideo(r, t, video); err != nil {
				return nil, err
			}
		}
		if t.Number == 0 {
			return nil, report.NewError(report.TrackDependency, "missing_track_number", "", entry.Offset, entry.Offset,
				"TrackEntry has no nonzero TrackNumber")
		}
		if _, exists := g.Tracks[t.Number]; exists {
			return nil, report.NewError(report.TrackDependency, "duplicate_track_number", "", entry.Offset, t.NumberOffset,
				"track number %d is declared more than once", t.Number)
		}
		g.Tracks[t.Number] = t
		g.Order = append(g.Order, t.Number)
	}
	return g, nil
}

func extractAudio(r *ebml.Reader, t *Track, audio *ebml.Element) *report.Error {
	if el := ebml.FindFirst(audio, ebml.IDChannels); el != nil {
		if v, err := r.ReadUint(el); err == nil {
			t.Channels, t.ChannelsOffset = v, el.Offset
		}
	}
	if el := ebml.FindFirst(audio, ebml.IDChannelLayout); el != nil {
		if v, err := r.ReadUint(el); err == nil {
			t.Layout, t.LayoutOffset = v, el.Offset
		}
	}
	if el := ebml.FindFirst(audio, ebml.IDSamplingFreq); el != nil {
		if v, err := r.ReadFloat(el); err == nil {
			t.SampleRate, t.SampleRateOff = v, el.Offset
		}
	}
	if el := ebml.FindFirst(audio, ebml.IDOutputSampling); el != nil {
		if v, err := r.ReadFloat(el); err == nil {
			t.OutputRate, t.OutputRateOff = v, el.Offset
		}
	}
	if el := ebml.FindFirst(audio, ebml.IDBitDepth); el != nil {
		if v, err := r.ReadUint(el); err == nil {
			t.BitDepth = v
		}
	}
	return nil
}

func extractVideo(r *ebml.Reader, t *Track, video *ebml.Element) *report.Error {
	if el := ebml.FindFirst(video, ebml.IDPixelWidth); el != nil {
		if v, err := r.ReadUint(el); err == nil {
			t.PixelWidth = v
		}
	}
	if el := ebml.FindFirst(video, ebml.IDPixelHeight); el != nil {
		if v, err := r.ReadUint(el); err == nil {
			t.PixelHeight = v
		}
	}
	if colour := ebml.FindFirst(video, ebml.IDColour); colour != nil {
		c := &ColorMetadata{Element: colour}
		if el := ebml.FindFirst(colour, ebml.IDMatrixCoeffs); el != nil {
			if v, err := r.ReadUint(el); err == nil {
				c.Matrix, c.MatrixOffset = v, el.Offset
			}
		}
		if el := ebml.FindFirst(colour, ebml.IDTransferChars); el != nil {
			if v, err := r.ReadUint(el); err == nil {
				c.Transfer, c.TransferOffset = v, el.Offset
			}
		}
		if el := ebml.FindFirst(colour, ebml.IDColourPrimaries); el != nil {
			if v, err := r.ReadUint(el); err == nil {
				c.Primaries, c.PrimariesOffset = v, el.Offset
			}
		}
		t.Color = c
	}
	return nil
}
