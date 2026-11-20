package bmff

import (
	"bytes"
	"fmt"

	mp4 "github.com/abema/go-mp4"
)

// This file adapts github.com/abema/go-mp4 for field-level decoding of
// known box types. Box boundaries always come from box.go; the adapter is
// used to decode and cross-check fields inside boxes the walker found.

// unmarshal decodes a full box payload (including version/flags) into dst.
func unmarshal(payload []byte, dst mp4.IBox) error {
	r := bytes.NewReader(payload)
	if _, err := mp4.Unmarshal(r, uint64(len(payload)), dst, mp4.Context{}); err != nil {
		return fmt.Errorf("go-mp4 decode %T: %w", dst, err)
	}
	return nil
}

// MvhdFields holds the mvhd fields this tool relies on.
type MvhdFields struct {
	Timescale uint32
	Duration  uint64
}

// DecodeMvhd decodes an mvhd box located by the walker.
func (r *Reader) DecodeMvhd(b Box) (MvhdFields, error) {
	if b.Type != "mvhd" {
		return MvhdFields{}, fmt.Errorf("%w: DecodeMvhd called on %s", ErrCorrupt, b.Path)
	}
	data, err := r.ReadBoxData(b, MaxMetaBox)
	if err != nil {
		return MvhdFields{}, err
	}
	var mvhd mp4.Mvhd
	if err := unmarshal(data, &mvhd); err != nil {
		return MvhdFields{}, err
	}
	return MvhdFields{Timescale: mvhd.Timescale, Duration: mvhd.GetDuration()}, nil
}

// TkhdFields holds the tkhd fields this tool relies on.
type TkhdFields struct {
	TrackID  uint32
	Duration uint64
}

// DecodeTkhd decodes a tkhd box located by the walker.
func (r *Reader) DecodeTkhd(b Box) (TkhdFields, error) {
	if b.Type != "tkhd" {
		return TkhdFields{}, fmt.Errorf("%w: DecodeTkhd called on %s", ErrCorrupt, b.Path)
	}
	data, err := r.ReadBoxData(b, MaxMetaBox)
	if err != nil {
		return TkhdFields{}, err
	}
	var tkhd mp4.Tkhd
	if err := unmarshal(data, &tkhd); err != nil {
		return TkhdFields{}, err
	}
	return TkhdFields{TrackID: tkhd.TrackID, Duration: tkhd.GetDuration()}, nil
}

// MdhdFields holds the mdhd fields this tool relies on.
type MdhdFields struct {
	Timescale uint32
	Duration  uint64
}

// DecodeMdhd decodes an mdhd box located by the walker.
func (r *Reader) DecodeMdhd(b Box) (MdhdFields, error) {
	if b.Type != "mdhd" {
		return MdhdFields{}, fmt.Errorf("%w: DecodeMdhd called on %s", ErrCorrupt, b.Path)
	}
	data, err := r.ReadBoxData(b, MaxMetaBox)
	if err != nil {
		return MdhdFields{}, err
	}
	var mdhd mp4.Mdhd
	if err := unmarshal(data, &mdhd); err != nil {
		return MdhdFields{}, err
	}
	return MdhdFields{Timescale: mdhd.Timescale, Duration: mdhd.GetDuration()}, nil
}

// CrossCheck compares our own parse of a box against go-mp4's decode and
// reports a corruption error when they disagree.
func CrossCheck(what string, ours, theirs uint64) error {
	if ours != theirs {
		return fmt.Errorf("%w: %s mismatch: own parse %d, go-mp4 %d",
			ErrCorrupt, what, ours, theirs)
	}
	return nil
}
