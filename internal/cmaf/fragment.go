package cmaf

import (
	"errors"
	"fmt"
	"io"
	"os"

	"example.com/archiveverify/internal/model"
)

// Sample is one trun entry with resolved defaults.
type Sample struct {
	Index                int    `json:"index"`
	Duration             uint32 `json:"duration"`
	Size                 uint32 `json:"size"`
	Flags                uint32 `json:"flags"`
	Sync                 bool   `json:"sync"`
	CompositionOffset    int64  `json:"composition_offset"`
	HasCompositionOffset bool   `json:"has_composition_offset"`
	ByteOffset           int64  `json:"byte_offset,omitempty"`
}

// TrackFragment is one traf/trun group.
type TrackFragment struct {
	TrackID         uint32   `json:"track_id"`
	Timescale       uint32   `json:"timescale,omitempty"`
	BaseDecodeTime  uint64   `json:"base_decode_time"`
	TFDTVersion     uint8    `json:"tfdt_version"`
	TFDTOffset      int64    `json:"tfdt_offset"`
	BaseIsMoof      bool     `json:"base_is_moof,omitempty"`
	DataOffset      int32    `json:"data_offset,omitempty"`
	TREXDefaults    Defaults `json:"-"`
	DefaultDuration uint32   `json:"default_duration"`
	DefaultSize     uint32   `json:"default_size"`
	DefaultFlags    uint32   `json:"default_flags"`
	Samples         []Sample `json:"samples"`
}

// Defaults are init trex defaults used when tfhd does not override them.
type Defaults struct {
	Duration uint32
	Size     uint32
	Flags    uint32
}

// Fragment is a parsed moof plus its associated mdat provenance.
type Fragment struct {
	Path        string           `json:"path"`
	Source      string           `json:"source"`
	Sequence    uint32           `json:"sequence"`
	HasSequence bool             `json:"has_sequence"`
	Moof        BoxHeader        `json:"moof"`
	Mdat        *BoxHeader       `json:"mdat,omitempty"`
	Boxes       []BoxHeader      `json:"boxes,omitempty"`
	Tracks      []*TrackFragment `json:"tracks"`
}

// TotalDuration returns the sum of sample durations for one track.
func (f *Fragment) TotalDuration(trackID uint32) uint64 {
	for _, tr := range f.Tracks {
		if tr.TrackID != trackID {
			continue
		}
		var total uint64
		for _, s := range tr.Samples {
			total += uint64(s.Duration)
		}
		return total
	}
	return 0
}

// ParseFragmentFile parses mfhd/traf/tfdt/trun and validates top-level bounds.
func ParseFragmentFile(path, source string, init *Init) (*Fragment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewParseError(source, "", -1, -1, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, model.NewParseError(source, "", -1, -1, err)
	}
	if !st.Mode().IsRegular() {
		return nil, model.NewParseError(source, "", 0, 0, errors.New("not a regular file"))
	}
	return ParseFragment(f, source, st.Size(), init, MaxBoxSize)
}

// ParseFragment parses from a ReaderAt. maxBox is normally MaxBoxSize.
func ParseFragment(r io.ReaderAt, source string, size int64, init *Init, maxBox int64) (*Fragment, error) {
	boxes, err := ScanBoxes(r, source, size, maxBox)
	if err != nil {
		return nil, err
	}
	moofIdx := -1
	out := &Fragment{Source: source, Boxes: boxes}
	for i, b := range boxes {
		switch b.Type {
		case "moof":
			if moofIdx != -1 {
				return nil, model.NewParseError(source, b.Type, b.Offset, b.Offset,
					errors.New("multiple moof boxes in fragment"))
			}
			moofIdx = i
			out.Moof = b
		case "mdat":
			b := b
			out.Mdat = &b
		}
	}
	if moofIdx == -1 {
		return nil, model.NewParseError(source, "", 0, firstUnreadableForNoMoof(boxes, size),
			errors.New("moof box not found"))
	}

	children, err := ScanRange(r, source, out.Moof.PayloadOffset, out.Moof.End, maxBox)
	if err != nil {
		return nil, err
	}
	for _, b := range children {
		switch b.Type {
		case "mfhd":
			seq, err := parseMFHD(r, source, b)
			if err != nil {
				return nil, err
			}
			out.Sequence = seq
			out.HasSequence = true
		case "traf":
			tf, err := parseTRAF(r, source, b, out.Moof.Offset, init, maxBox)
			if err != nil {
				return nil, err
			}
			out.Tracks = append(out.Tracks, tf)
		}
	}
	if len(out.Tracks) == 0 {
		return nil, model.NewParseError(source, "traf", out.Moof.Offset, out.Moof.Offset,
			errors.New("moof contains no traf/trun"))
	}
	if err := validateMDAT(r, source, out); err != nil {
		return nil, err
	}
	return out, nil
}

func firstUnreadableForNoMoof(boxes []BoxHeader, size int64) int64 {
	if len(boxes) == 0 {
		return 0
	}
	return size
}

func validateMDAT(r io.ReaderAt, source string, f *Fragment) error {
	if f.Mdat == nil {
		return model.NewParseError(source, "mdat", f.Moof.Offset, f.Moof.End,
			errors.New("mdat box not found"))
	}
	// The fixture and accepted inputs use in-mdat sample offsets. Resolve the
	// maximum trun-declared extent and ensure it stays inside the file.
	for _, tr := range f.Tracks {
		var sum uint64
		for _, s := range tr.Samples {
			sum += uint64(s.Size)
			if sum > uint64(f.Mdat.PayloadSize) && f.Mdat.PayloadSize > 0 {
				return model.NewParseError(source, "trun", tr.TFDTOffset, f.Mdat.End,
					fmt.Errorf("track %d sample bytes %d exceed mdat payload %d",
						tr.TrackID, sum, f.Mdat.PayloadSize))
			}
		}
	}
	return nil
}
