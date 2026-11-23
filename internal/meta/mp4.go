package meta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediad/internal/clock"
)

// mp4Acc accumulates boxes of one trak while the structure walk proceeds.
type mp4Acc struct {
	trackID   uint32
	timescale uint32
	width     uint32
	height    uint32
	matrix    [9]int32
	colour    *Colour
	crop      *Crop
	sampleN   uint32
	sampleSz  uint32 // uniform size; 0 means per-entry sizes (not in subset)
	chunkOff  uint64
	hasChunk  bool
}

// mdatExtent records the mdat byte span for sample-range validation.
type mdatExtent struct {
	start, end uint64
}

// ParseMP4 reads the supported MP4 subset and returns one description per
// valid track. A corrupted track (illegal sample range, unreadable box) is
// isolated: it is omitted from the result and reported as a recoverable
// clock.Error, while other tracks are still returned. A truncated stream
// likewise yields a recoverable error plus the tracks already parsed.
func ParseMP4(r io.ReadSeeker) ([]*TrackDescription, error) {
	var (
		tracks []*mp4Acc
		varErr error
		mdat   *mdatExtent
	)
	streamEnd, seekErr := r.Seek(0, io.SeekEnd)
	if seekErr != nil {
		return nil, clock.WrapError(clock.StageMP4, 0, false, seekErr)
	}
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		boxType := h.BoxInfo.Type
		switch boxType {
		case mp4.BoxTypeTrak():
			tracks = append(tracks, &mp4Acc{trackID: uint32(len(tracks) + 1)})
			return h.Expand()
		case mp4.BoxTypeMdat():
			mdat = &mdatExtent{
				start: h.BoxInfo.Offset + h.BoxInfo.HeaderSize,
				end:   h.BoxInfo.Offset + h.BoxInfo.Size,
			}
			return nil, nil // payload is opaque synthetic media, do not walk it
		case mp4.BoxTypeMdhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if mdhd, ok := box.(*mp4.Mdhd); ok && len(tracks) > 0 {
				tracks[len(tracks)-1].timescale = mdhd.Timescale
			}
			return nil, nil
		case mp4.BoxTypeTkhd():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if tkhd, ok := box.(*mp4.Tkhd); ok && len(tracks) > 0 {
				cur := tracks[len(tracks)-1]
				cur.trackID = tkhd.TrackID
				cur.matrix = tkhd.Matrix
			}
			return nil, nil
		case mp4.BoxTypeAvc1():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if vse, ok := box.(*mp4.VisualSampleEntry); ok && len(tracks) > 0 {
				cur := tracks[len(tracks)-1]
				cur.width = uint32(vse.Width)
				cur.height = uint32(vse.Height)
			}
			return h.Expand() // walk into avc1 to reach the colr child
		case mp4.BoxTypeColr():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if colr, ok := box.(*mp4.Colr); ok && len(tracks) > 0 &&
				colr.ColourType == [4]byte{'n', 'c', 'l', 'x'} {
				tracks[len(tracks)-1].colour = &Colour{
					Primaries: colr.ColourPrimaries,
					Transfer:  colr.TransferCharacteristics,
					Matrix:    colr.MatrixCoefficients,
					FullRange: colr.FullRangeFlag,
				}
			}
			return nil, nil
		case mp4.BoxTypeStts(), mp4.BoxTypeStss(), mp4.BoxTypeStsc():
			_, _, perr := h.ReadPayload() // structural validation only
			return nil, perr
		case mp4.BoxTypeStsz():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if stsz, ok := box.(*mp4.Stsz); ok && len(tracks) > 0 {
				cur := tracks[len(tracks)-1]
				cur.sampleN = stsz.SampleCount
				cur.sampleSz = stsz.SampleSize
			}
			return nil, nil
		case mp4.BoxTypeStco():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if stco, ok := box.(*mp4.Stco); ok && len(tracks) > 0 && stco.EntryCount > 0 {
				cur := tracks[len(tracks)-1]
				cur.chunkOff = uint64(stco.ChunkOffset[0])
				cur.hasChunk = true
			}
			return nil, nil
		case mp4.BoxTypeCo64():
			box, _, perr := h.ReadPayload()
			if perr != nil {
				return nil, perr
			}
			if co64, ok := box.(*mp4.Co64); ok && len(tracks) > 0 && co64.EntryCount > 0 {
				cur := tracks[len(tracks)-1]
				cur.chunkOff = co64.ChunkOffset[0]
				cur.hasChunk = true
			}
			return nil, nil
		case mp4.StrToBoxType("crp0"):
			var buf bytes.Buffer
			if _, perr := h.ReadData(&buf); perr != nil {
				return nil, perr
			}
			raw := buf.Bytes()
			if len(raw) < 16 {
				return nil, clock.NewError(clock.StageMP4, "crp0 crop box shorter than 16 bytes", true)
			}
			if len(tracks) == 0 {
				return nil, clock.NewError(clock.StageMP4, "crp0 crop box outside any trak", true)
			}
			tracks[len(tracks)-1].crop = &Crop{
				X:      int64(binary.BigEndian.Uint32(raw[0:])),
				Y:      int64(binary.BigEndian.Uint32(raw[4:])),
				Width:  int64(binary.BigEndian.Uint32(raw[8:])),
				Height: int64(binary.BigEndian.Uint32(raw[12:])),
			}
			return nil, nil
		default:
			if isContainer(boxType) {
				return h.Expand()
			}
			return nil, nil
		}
	})
	if err != nil {
		// Truncated/short reads and marshalling failures are input corruption.
		return accsToDescs(tracks), clock.WrapError(clock.StageMP4, 0, true,
			fmt.Errorf("mp4 structure walk failed: %w", err))
	}
	// A cut inside mdat does not fail the box walk (the payload is opaque),
	// so verify the declared extent against the real stream length.
	if mdat != nil && mdat.end > uint64(streamEnd) {
		varErr = errors.Join(varErr, clock.NewError(clock.StageMP4,
			fmt.Sprintf("truncated mdat: declared end %d exceeds stream length %d",
				mdat.end, streamEnd), true))
	}
	out := make([]*TrackDescription, 0, len(tracks))
	for _, acc := range tracks {
		if verr := validateSampleRange(acc, mdat); verr != nil {
			varErr = errors.Join(varErr, verr)
			continue // isolate the corrupted track, keep the others
		}
		out = append(out, acc.toDescription())
	}
	return out, varErr
}

// validateSampleRange checks chunk offset and sample extent against mdat.
func validateSampleRange(a *mp4Acc, mdat *mdatExtent) error {
	if !a.hasChunk || mdat == nil {
		return clock.TrackError(clock.StageMP4, a.trackID, "track missing chunk offset or mdat", true)
	}
	end := a.chunkOff + uint64(a.sampleN)*uint64(a.sampleSz)
	if a.chunkOff < mdat.start || end > mdat.end {
		return clock.TrackError(clock.StageMP4, a.trackID,
			fmt.Sprintf("illegal sample range: [%d,%d) outside mdat [%d,%d)",
				a.chunkOff, end, mdat.start, mdat.end), true)
	}
	return nil
}

func (a *mp4Acc) toDescription() *TrackDescription {
	d := &TrackDescription{
		Container: KindMP4,
		TrackID:   a.trackID,
		Timescale: a.timescale,
		Width:     a.width,
		Height:    a.height,
		Colour:    a.colour,
		Crop:      a.crop,
	}
	if deg := matrixDegrees(a.matrix); deg != 0 {
		d.Rotation = &Rotation{Degrees: deg}
	}
	return d
}

func accsToDescs(accs []*mp4Acc) []*TrackDescription {
	out := make([]*TrackDescription, 0, len(accs))
	for _, a := range accs {
		out = append(out, a.toDescription())
	}
	return out
}

// matrixDegrees decodes the 16.16 fixed-point display matrix into a clockwise
// rotation in {0,90,180,270}; non-orthogonal matrices are reported as 0.
func matrixDegrees(m [9]int32) int32 {
	const one = 0x00010000
	switch {
	case m[0] == one && m[4] == one:
		return 0
	case m[0] == 0 && m[1] == one && m[3] == -one && m[4] == 0:
		return 90
	case m[0] == -one && m[4] == -one:
		return 180
	case m[0] == 0 && m[1] == -one && m[3] == one && m[4] == 0:
		return 270
	default:
		return 0
	}
}

// isContainer reports whether a box type is walked into by the subset parser.
func isContainer(t mp4.BoxType) bool {
	switch t {
	case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
		mp4.BoxTypeDinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(),
		mp4.BoxTypeFtyp():
		return true
	default:
		return false
	}
}
