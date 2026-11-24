package probe

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"archive/internal/clock"
)

// maxSamples bounds sample-table expansion so hostile files cannot exhaust
// memory. Larger tables are rejected rather than silently truncated.
const maxSamples = 2_000_000

// ProbeMP4File parses an MP4 from disk.
func ProbeMP4File(path string) (*Container, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.New(clock.KindInvalidMetadata, "open mp4").WithLocation(path).WithCause(err)
	}
	defer f.Close()
	return ProbeMP4(f)
}

// ProbeMP4 parses track metadata from an MP4 byte stream.
func ProbeMP4(r io.ReadSeeker) (*Container, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, clock.New(clock.KindInvalidMetadata, "seek mp4").WithCause(err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, clock.New(clock.KindInvalidMetadata, "rewind mp4").WithCause(err)
	}
	c := &Container{Format: "mp4"}
	var current *Track

	_, err = mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
			mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeEdts():
			_, err := h.Expand()
			return nil, err
		case mp4.BoxTypeMvhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, err)
			}
			mvhd := box.(*mp4.Mvhd)
			if mvhd.Timescale == 0 {
				return nil, clock.New(clock.KindInvalidMetadata, "mvhd timescale is zero").
					WithLocation(offsetOf(h))
			}
			c.Timescale = mvhd.Timescale
			c.DurationNS = unitsToNS(mvhd.GetDuration(), mvhd.Timescale)
		case mp4.BoxTypeTkhd():
			t, err := newVideoTrack(h, len(c.Tracks))
			if err != nil {
				return nil, err
			}
			c.Tracks = append(c.Tracks, t)
			current = t
		case mp4.BoxTypeMdhd():
			if current == nil {
				return nil, nil
			}
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, err).(*clock.Error).WithTrack(current.ID)
			}
			mdhd := box.(*mp4.Mdhd)
			if mdhd.Timescale == 0 {
				return nil, clock.New(clock.KindInvalidMetadata, "mdhd timescale is zero").
					WithTrack(current.ID).WithLocation(offsetOf(h))
			}
			current.Timescale = mdhd.Timescale
			current.DurationUnits = int64(mdhd.GetDuration())
			current.DurationNS = unitsToNS(mdhd.GetDuration(), mdhd.Timescale)
		case mp4.BoxTypeElst():
			if current == nil {
				return nil, nil
			}
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, err).(*clock.Error).WithTrack(current.ID)
			}
			elst := box.(*mp4.Elst)
			if len(elst.Entries) > 0 {
				var mt int64
				if elst.GetVersion() == 0 {
					mt = int64(elst.Entries[0].MediaTimeV0)
				} else {
					mt = elst.Entries[0].MediaTimeV1
				}
				if mt < 0 {
					current.EditStartNS = 0
					current.AddWarning("negative edit media_time %d treated as empty edit", mt)
				} else {
					current.EditStartNS = unitsToNS(uint64(mt), current.Timescale)
				}
			}
		case mp4.BoxTypeStsd():
			_, err := h.Expand()
			return nil, err
		case mp4.BoxTypeAvc1(), mp4.BoxTypeMp4v(), mp4.BoxTypeHvc1(), mp4.BoxTypeHev1(), mp4.BoxTypeEncv():
			if current == nil {
				return nil, nil
			}
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, err).(*clock.Error).WithTrack(current.ID)
			}
			vse := box.(*mp4.VisualSampleEntry)
			current.StoredWidth = int(vse.Width)
			current.StoredHeight = int(vse.Height)
			_, err = h.Expand()
			return nil, err
		case mp4.BoxTypeColr():
			if current == nil {
				return nil, nil
			}
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, err).(*clock.Error).WithTrack(current.ID)
			}
			colr := box.(*mp4.Colr)
			if string(colr.ColourType[:]) == "nclx" {
				current.Colors = append(current.Colors, ColorDecl{
					Primaries: colr.ColourPrimaries,
					Transfer:  colr.TransferCharacteristics,
					Matrix:    colr.MatrixCoefficients,
					FullRange: colr.FullRangeFlag,
					Source:    SourceColr,
				})
			} else {
				current.AddWarning("unsupported colr type %q preserved as warning", string(colr.ColourType[:]))
			}
		case mp4.StrToBoxType("clap"):
			if current == nil {
				return nil, nil
			}
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, err).(*clock.Error).WithTrack(current.ID)
			}
			clap := box.(*mp4ClapBox)
			crop, cerr := cropFromClap(clap, current.StoredWidth, current.StoredHeight)
			if cerr != nil {
				return nil, cerr.WithTrack(current.ID).WithLocation(offsetOf(h))
			}
			current.Crops = append(current.Crops, crop)
		case mp4.BoxTypeStts():
			if current == nil {
				return nil, nil
			}
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, err).(*clock.Error).WithTrack(current.ID)
			}
			stts := box.(*mp4.Stts)
			var total uint64
			for _, e := range stts.Entries {
				total += uint64(e.SampleCount)
			}
			if total > maxSamples {
				return nil, clock.Newf(clock.KindOutOfRange, "sample count %d exceeds limit %d", total, maxSamples).
					WithTrack(current.ID).WithLocation(offsetOf(h))
			}
			current.SampleCount = int(total)
			if total > 0 && len(stts.Entries) > 0 {
				current.SampleIntervalNS = unitsToNS(uint64(stts.Entries[0].SampleDelta), current.Timescale)
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, classifyTruncation(err, size)
	}
	if err := verifyBoxBounds(r, size); err != nil {
		return nil, err
	}
	for _, t := range c.Tracks {
		if t.StoredWidth == 0 || t.StoredHeight == 0 {
			return nil, clock.New(clock.KindInvalidMetadata, "video sample entry missing or zero-sized").
				WithTrack(t.ID).WithLocation(fmt.Sprintf("offset %d", t.Offset))
		}
	}
	return c, nil
}

func offsetOf(h *mp4.ReadHandle) string { return fmt.Sprintf("offset %d", h.BoxInfo.Offset) }

// verifyBoxBounds re-walks the root boxes and rejects any whose declared size
// exceeds the file. This catches truncation that leaves the box header intact.
func verifyBoxBounds(r io.ReadSeeker, size int64) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for {
		bi, err := mp4.ReadBoxInfo(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return classifyTruncation(err, size)
		}
		if int64(bi.Offset+bi.Size) > size {
			return clock.Newf(clock.KindTruncated,
				"box %s declares %d bytes but only %d remain", bi.Type, bi.Size, size-int64(bi.Offset)).
				WithLocation(fmt.Sprintf("offset %d", bi.Offset))
		}
		if _, err := bi.SeekToEnd(r); err != nil {
			return classifyTruncation(err, size)
		}
	}
}

func truncErr(h *mp4.ReadHandle, err error) error {
	return clock.Newf(clock.KindTruncated, "cannot read %s payload: %v", h.BoxInfo.Type, err).
		WithLocation(offsetOf(h)).WithCause(err)
}

func classifyTruncation(err error, size int64) error {
	if err == nil {
		return nil
	}
	var ce *clock.Error
	if errors.As(err, &ce) {
		return err
	}
	return clock.Newf(clock.KindTruncated, "mp4 parse failed near EOF (%d bytes): %v", size, err).WithCause(err)
}

func unitsToNS(units uint64, timescale uint32) int64 {
	if timescale == 0 || units > uint64(1)<<62 {
		return 0
	}
	tb, _ := clock.NewTimebase(timescale)
	ns, err := tb.ToNanos(int64(units))
	if err != nil {
		return 0
	}
	return ns
}

func newVideoTrack(h *mp4.ReadHandle, index int) (*Track, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, truncErr(h, err).(*clock.Error)
	}
	tkhd := box.(*mp4.Tkhd)
	rot, err := rotationFromMatrix(tkhd.Matrix)
	if err != nil {
		return nil, err
	}
	return &Track{
		ID:            fmt.Sprintf("mp4:%d", index),
		Format:        "mp4",
		Kind:          "video",
		Index:         index,
		Offset:        h.BoxInfo.Offset,
		DisplayWidth:  int(tkhd.GetWidth()),
		DisplayHeight: int(tkhd.GetHeight()),
		Rotations:     []RotationDecl{{Degrees: rot, Source: SourceTkhd}},
	}, nil
}

// rotationFromMatrix derives clockwise display rotation from a 16.16 track
// matrix. Only right-angle rotations are representable; anything else is an
// explicit error rather than a guess.
func rotationFromMatrix(m [9]int32) (int, error) {
	const one = int32(0x10000)
	a, b, c, d := m[0], m[1], m[3], m[4]
	switch {
	case near(a, one) && near(b, 0) && near(c, 0) && near(d, one):
		return 0, nil
	case near(a, 0) && near(b, one) && near(c, -one) && near(d, 0):
		return 90, nil
	case near(a, -one) && near(b, 0) && near(c, 0) && near(d, -one):
		return 180, nil
	case near(a, 0) && near(b, -one) && near(c, one) && near(d, 0):
		return 270, nil
	}
	return 0, clock.Newf(clock.KindUnsupported, "non right-angle or sheared track matrix [%d %d %d %d]", a, b, c, d)
}

func near(v, want int32) bool {
	d := v - want
	if d < 0 {
		d = -d
	}
	return d <= 2 // tolerate fixed-point rounding
}
