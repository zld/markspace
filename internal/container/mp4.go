// Package container reads MP4 container and track constraints with go-mp4.
// It never silently repairs anything: missing tracks, time-base mismatches
// and declared-boundary drift are counted separately as model issues.
package container

import (
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"packcheck/internal/model"
)

// TrackInfo is the per-track view used by downstream validation.
type TrackInfo struct {
	TrackID   uint32
	Timescale uint32
	StartTick uint64 // edit-list media start, zero without an elst
	Samples   uint32
}

// Boundary is one declared sidx reference on the shared clock.
type Boundary struct {
	StartTick uint64
	EndTick   uint64
}

// Container is the parsed, normalized view of an MP4 stream.
type Container struct {
	MovieTimescale uint32
	Tracks         []TrackInfo
	Boundaries     []Boundary
	DeclaredSamples uint32 // sum of sidx subsegment durations in frames
}

// ValidationResult bundles the container view and the counted defects.
type ValidationResult struct {
	Container *Container
	Issues    model.IssueLog
}

// Parse decodes the boxes needed for track and boundary validation.
// A hard parse failure is returned as an error; constraint failures appear
// in the issue log so callers can still produce an auditable conclusion.
func Parse(r io.ReadSeeker) (*Container, error) {
	c := &Container{}
	var sidxOff int64 = -1
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		path := h.Path
		last := path[len(path)-1]
		switch {
		case last == mp4.BoxTypeMvhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			c.MovieTimescale = box.(*mp4.Mvhd).Timescale
			return nil, nil
		case last == mp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			c.Tracks = append(c.Tracks, TrackInfo{TrackID: box.(*mp4.Tkhd).TrackID})
			return nil, nil
		case last == mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if len(c.Tracks) == 0 {
				return nil, fmt.Errorf("mdhd without tkhd at offset %d", h.BoxInfo.Offset)
			}
			c.Tracks[len(c.Tracks)-1].Timescale = box.(*mp4.Mdhd).Timescale
			return nil, nil
		case last == mp4.BoxTypeElst():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			elst := box.(*mp4.Elst)
			if len(elst.Entries) > 0 && len(c.Tracks) > 0 {
				e := elst.Entries[0]
				if elst.Version == 1 {
					c.Tracks[len(c.Tracks)-1].StartTick = uint64(e.MediaTimeV1)
				} else {
					c.Tracks[len(c.Tracks)-1].StartTick = uint64(e.MediaTimeV0)
				}
			}
			return nil, nil
		case last == mp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if len(c.Tracks) > 0 {
				c.Tracks[len(c.Tracks)-1].Samples = box.(*mp4.Stsz).SampleCount
			}
			return nil, nil
		case last == mp4.BoxTypeSidx():
			sidxOff = int64(h.BoxInfo.Offset)
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			sidx := box.(*mp4.Sidx)
			cursor := sidx.GetEarliestPresentationTime()
			for _, ref := range sidx.References {
				c.Boundaries = append(c.Boundaries, Boundary{
					StartTick: cursor,
					EndTick:   cursor + uint64(ref.SubsegmentDuration),
				})
				cursor += uint64(ref.SubsegmentDuration)
				c.DeclaredSamples += ref.SubsegmentDuration
			}
			return nil, nil
		default:
			return h.Expand()
		}
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, fmt.Errorf("mp4 parse failure (sidx offset %d): %w", sidxOff, err)
	}
	if c.MovieTimescale == 0 {
		return nil, fmt.Errorf("mp4 constraint failure: missing mvhd time base")
	}
	return c, nil
}
// Validate applies the declared container constraints and counts each defect
// class separately instead of repairing it.
func Validate(c *Container, expectedTracks int) *ValidationResult {
	res := &ValidationResult{Container: c}
	if len(c.Tracks) < expectedTracks {
		res.Issues.Add(model.Issue{
			Code: model.CodeMissingTrack, Severity: model.SeverityError,
			Layer: model.LayerContainer, ByteOffset: 0,
			Message: fmt.Sprintf("tracks=%d expected=%d", len(c.Tracks), expectedTracks),
		})
	}
	for _, tr := range c.Tracks {
		if tr.Timescale != c.MovieTimescale {
			res.Issues.Add(model.Issue{
				Code: model.CodeTimebaseMismatch, Severity: model.SeverityError,
				Layer: model.LayerContainer, Track: fmt.Sprintf("track-%d", tr.TrackID),
				ByteOffset: 0,
				Message: fmt.Sprintf("track timescale %d != movie timescale %d",
					tr.Timescale, c.MovieTimescale),
			})
		}
	}
	var actual uint32
	for _, tr := range c.Tracks {
		actual += tr.Samples
	}
	if c.DeclaredSamples != actual {
		res.Issues.Add(model.Issue{
			Code: model.CodeBoundaryDrift, Severity: model.SeverityError,
			Layer: model.LayerContainer, ByteOffset: 0,
			Message: fmt.Sprintf("sidx declares %d frames, sample tables hold %d (drift=%d)",
				c.DeclaredSamples, actual, int64(c.DeclaredSamples)-int64(actual)),
		})
	}
	return res
}
