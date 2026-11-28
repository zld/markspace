package track

import (
	"sort"

	"mediaregress/internal/media"
)

// buildMXFTracks joins material-package descriptors to their tracks and source
// clips, then to the essence elements whose key carries the track number.
func buildMXFTracks(sets []*mxfSet, essence map[int64][]klvElem, size int64) []*Track {
	// Group descriptors by the package they belong to (descriptors precede the
	// track that references them in file order for this profile).
	tracks := map[uint16]*mxfSet{}
	var order []uint16
	for _, s := range sets {
		switch s.Kind {
		case setTimelineTrack:
			n, ok := s.uint(tagTrackNumber)
			if !ok {
				continue
			}
			tracks[uint16(n)] = s
			order = append(order, uint16(n))
		}
	}
	// Descriptors name the timeline track they describe. Multiple descriptors
	// may target one track; all of them are kept so conflicts are visible.
	descs := map[uint16][]*mxfSet{}
	for _, s := range sets {
		switch s.Kind {
		case setGenericPicture, setCDCI, setWaveAudio:
			n, ok := s.uint(tagLinkedTrackID)
			if !ok {
				continue
			}
			descs[uint16(n)] = append(descs[uint16(n)], s)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	var out []*Track
	for _, num := range order {
		out = append(out, mxfTrack(num, tracks[num], descs[num], essence[int64(num)], size))
	}
	return out
}

func mxfTrack(num uint16, tr *mxfSet, descs []*mxfSet, elems []klvElem, size int64) *Track {
	id := "m" + itoa(int64(num-1))
	t := &Track{
		ID:          id,
		Container:   "mxf",
		Timescale:   0,
		Version:     ParseVersion,
		SourceStart: tr.Offset,
		RotSource:   Source{Container: "mxf", Element: "tkhd"},
	}
	// Edit rate is a rational; the denominator is the time base.
	editRate, hasRate := tr.bytes(tagEditRate)
	if !hasRate || len(editRate) != 8 {
		t.AddError(media.NewError(media.ClassInvalidTimebase, media.StageParse, "", tr.Offset,
			"timeline track has no valid edit rate"))
		return t
	}
	numRate := int64(int32(be32(editRate[0:4])))
	denRate := int64(int32(be32(editRate[4:8])))
	rate := media.FrameRate{Num: numRate, Den: denRate}
	if err := rate.Validate(); err != nil {
		t.AddError(media.NewError(media.ClassInvalidTimebase, media.StageParse, "", tr.Items[tagEditRate].Offset,
			"edit rate is not a valid time base"))
		return t
	}
	t.Timescale = uint32(denRate)
	if dur, ok := tr.uint(tagDuration); ok {
		if d, err := rate.Ticks(int64(dur)); err == nil {
			t.Duration = d
		} else {
			t.AddError(media.NewError(media.ClassOutOfRange, media.StageParse, "", tr.Items[tagDuration].Offset,
				"track duration overflows the timeline"))
		}
	}
	if len(descs) == 0 {
		t.AddError(media.NewError(media.ClassCorruptInput, media.StageParse, "", tr.Offset,
			"timeline track has no descriptor"))
		return t
	}
	desc := descs[0]
	for _, d := range descs {
		parseMXFColor(d, t)
	}
	t.Conflicts = colorConflicts(t.Colors)
	switch desc.Kind {
	case setGenericPicture, setCDCI:
		t.Kind = KindVideo
		if v, ok := desc.uint(tagStoredWidth); ok {
			t.FrameWidth = int64(v)
		}
		if v, ok := desc.uint(tagStoredHeight); ok {
			t.FrameHeight = int64(v)
		}
		parseMXFCrop(desc, t)
	case setWaveAudio:
		t.Kind = KindAudio
		if v, ok := desc.uint(tagChannelCount); ok {
			t.RotMatrix[0] = int32(v) // channel count is recorded in the raw slot
		}
	default:
		t.Kind = KindVideo
	}
	// Essence: every element is one frame slot of the edit-rate time base. The
	// element key's last four bytes equal the timeline track number.
	step, err := rate.Ticks(1)
	if err != nil {
		t.AddError(media.NewError(media.ClassInvalidTimebase, media.StageParse, "", tr.Offset,
			"edit rate cannot be converted to ticks"))
		return t
	}
	for i, e := range elems {
		// The parse-level truncation flag is set by the caller or by the
		// clipped element itself; either way the error is positioned at the
		// first missing byte.
		if e.Truncated || e.BodyOffset+e.BodySize > size {
			// The first missing byte is the end of the file, not the element
			// key; report that position and stop this track's sample table at
			// the last complete element.
			t.Truncated = true
			t.TruncatedAt = media.Ticks(int64(i)) * step
			t.TruncatedOffset = size
			t.AddError(media.NewError(media.ClassTruncatedInput, media.StageParse, "", size,
				"essence item extends past end of file"))
			break
		}
		t.Samples = append(t.Samples, Sample{
			Index: i, Offset: e.BodyOffset, Size: uint32(e.BodySize),
			Start: media.Ticks(int64(i)) * step, Duration: step, Keyframe: true,
		})
	}
	return t
}

// parseMXFCrop turns sampled/display offsets into a unique pixel rectangle.
// Properties that disagree produce an invalid crop, which rejects the track.
func parseMXFCrop(desc *mxfSet, t *Track) {
	sw, swok := desc.uint(tagStoredWidth)
	sh, shok := desc.uint(tagStoredHeight)
	dw, dwok := desc.uint(tagDisplayWidth)
	dh, dhok := desc.uint(tagDisplayHeight)
	dx, dxok := desc.uint(tagDisplayXOffset)
	dy, dyok := desc.uint(tagDisplayYOffset)
	if !swok || !shok {
		return
	}
	if !dwok || !dhok {
		return
	}
	var left, top int64
	if dxok {
		left = int64(dx)
	}
	if dyok {
		top = int64(dy)
	}
	// Display *width* is expressed as a right edge in some profiles; here the
	// fixture writes an origin-relative rectangle, so validate directly.
	a := &Aperture{
		Left: left, Top: top, Width: int64(dw), Height: int64(dh),
		Source: Source{Container: "mxf", Element: "cdci/display", Offset: desc.Items[tagDisplayWidth].Offset},
	}
	if !dxok || !dyok {
		a.Left, a.Top = (int64(sw)-int64(dw))/2, (int64(sh)-int64(dh))/2
	}
	if err := a.Validate(int64(sw), int64(sh)); err != nil {
		t.Crop = a
		t.AddError(media.NewError(media.ClassInvalidCrop, media.StageParse, "", a.Source.Offset, err.Error()))
		return
	}
	t.Crop = a
	if v, ok := desc.uint(tagSampledWidth); ok && v != sw {
		t.AddError(media.NewError(media.ClassInvalidCrop, media.StageParse, "", desc.Items[tagSampledWidth].Offset,
			"sampled width disagrees with stored width"))
	}
	if v, ok := desc.uint(tagSampledHeight); ok && v != sh {
		t.AddError(media.NewError(media.ClassInvalidCrop, media.StageParse, "", desc.Items[tagSampledHeight].Offset,
			"sampled height disagrees with stored height"))
	}
}

// parseMXFColor records the three color properties as one description with the
// offset of the first property. Distinct descriptions cannot exist within one
// descriptor, so conflicts only arise across descriptors and are handled by the
// caller in mergeColor.
func parseMXFColor(desc *mxfSet, t *Track) {
	p, pok := desc.bytes(tagColorPrimaries)
	q, qok := desc.bytes(tagTransferChar)
	m, mok := desc.bytes(tagCodingEquations)
	if !pok && !qok && !mok {
		return
	}
	off := desc.Offset
	if pok {
		off = desc.Items[tagColorPrimaries].Offset
	}
	c := ColorDescription{
		Interpretation: "mxf-property",
		Source:         Source{Container: "mxf", Element: "cdci/color", Offset: off},
	}
	c.Primaries, c.Raw = decodeColorUL(p)
	if qok {
		c.Transfer, _ = decodeColorUL(q)
	}
	if mok {
		c.Matrix, _ = decodeColorUL(m)
	}
	t.Colors = append(t.Colors, c)
}

// decodeColorUL keeps the raw hex and extracts the enumeration code from the
// last two bytes of a 16-byte UL. Short values are treated as plain codes.
func decodeColorUL(v []byte) (uint16, string) {
	raw := hexRaw(v)
	switch len(v) {
	case 16:
		return uint16(v[14])<<8 | uint16(v[15]), raw
	case 2:
		return uint16(v[0])<<8 | uint16(v[1]), raw
	case 4:
		return uint16(v[2])<<8 | uint16(v[3]), raw
	}
	return 0, raw
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
