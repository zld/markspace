package manifest

import (
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"rtptrace/internal/timeline"
)

// MP4SampleTable is the subset of stts/stsz/stss used by the regression core.
type MP4SampleTable struct {
	TimeToSample []gomp4.SttsEntry `json:"time_to_sample"`
	SampleSize   uint32            `json:"sample_size"`
	SampleCount  uint32            `json:"sample_count"`
	EntrySizes   []uint32          `json:"entry_sizes,omitempty"`
	SyncSamples  []uint32          `json:"sync_samples,omitempty"`
}

// MP4Track is the go-mp4 decoded track metadata needed to cross-check JSON.
type MP4Track struct {
	ID          uint32             `json:"track_id"`
	Handler     string             `json:"handler"`
	Timescale   uint32             `json:"timescale"`
	Duration    uint64             `json:"duration"`
	EditList    *timeline.EditList `json:"edit_list,omitempty"`
	SampleTable MP4SampleTable     `json:"sample_table"`
}

// MP4File contains the parsed movie timescale and tracks.
type MP4File struct {
	MovieTimescale uint32     `json:"movie_timescale"`
	MovieDuration  uint64     `json:"movie_duration"`
	Tracks         []MP4Track `json:"tracks"`
}

// ReadMP4 parses tracks, edit lists and sample tables through go-mp4.
// The returned file can be non-nil together with a corruption error.
func ReadMP4(r io.ReadSeeker) (*MP4File, error) {
	f := &MP4File{}
	var current *MP4Track
	_, err := gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case gomp4.BoxTypeMoov(), gomp4.BoxTypeMdia(),
			gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeEdts():
			_, err := h.Expand()
			return nil, err
		case gomp4.BoxTypeTrak():
			f.Tracks = append(f.Tracks, MP4Track{})
			current = &f.Tracks[len(f.Tracks)-1]
			_, err := h.Expand()
			return nil, err
		}

		if current == nil {
			if h.BoxInfo.Type == gomp4.BoxTypeMvhd() {
				box, _, err := h.ReadPayload()
				if err != nil {
					return nil, err
				}
				mvhd := box.(*gomp4.Mvhd)
				f.MovieTimescale = mvhd.Timescale
				f.MovieDuration = mvhd.GetDuration()
			}
			return nil, nil
		}

		switch h.BoxInfo.Type {
		case gomp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapTrackCorruption(current.ID, 0, "tkhd", err)
			}
			current.ID = box.(*gomp4.Tkhd).TrackID
		case gomp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapTrackCorruption(current.ID, 0, "mdhd", err)
			}
			mdhd := box.(*gomp4.Mdhd)
			current.Timescale = mdhd.Timescale
			current.Duration = mdhd.GetDuration()
		case gomp4.BoxTypeHdlr():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapTrackCorruption(current.ID, 0, "hdlr", err)
			}
			current.Handler = string(box.(*gomp4.Hdlr).HandlerType[:])
		case gomp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapTrackCorruption(current.ID, current.SampleTable.SampleCount+1, "stts", err)
			}
			current.SampleTable.TimeToSample = box.(*gomp4.Stts).Entries
		case gomp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapTrackCorruption(current.ID, current.SampleTable.SampleCount+1, "stsz", err)
			}
			stsz := box.(*gomp4.Stsz)
			current.SampleTable.SampleSize = stsz.SampleSize
			current.SampleTable.SampleCount = stsz.SampleCount
			current.SampleTable.EntrySizes = stsz.EntrySize
		case gomp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapTrackCorruption(current.ID, 0, "stss", err)
			}
			current.SampleTable.SyncSamples = box.(*gomp4.Stss).SampleNumber
		case gomp4.BoxTypeElst():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, wrapTrackCorruption(current.ID, 0, "elst", err)
			}
			elst := box.(*gomp4.Elst)
			current.EditList = convertEditList(elst, f.MovieTimescale, current.Timescale)
		}
		return nil, nil
	})
	if err != nil {
		return f, err
	}
	return f, nil
}

func convertEditList(elst *gomp4.Elst, movieScale, mediaScale uint32) *timeline.EditList {
	l := &timeline.EditList{MovieTimescale: movieScale, MediaTimescale: mediaScale}
	for _, e := range elst.Entries {
		if elst.GetVersion() == 1 {
			l.Edits = append(l.Edits, timeline.Edit{
				SegmentDuration: int64(e.SegmentDurationV1),
				MediaTime:       e.MediaTimeV1,
			})
		} else {
			l.Edits = append(l.Edits, timeline.Edit{
				SegmentDuration: int64(e.SegmentDurationV0),
				MediaTime:       int64(e.MediaTimeV0),
			})
		}
	}
	return l
}

func wrapTrackCorruption(track, sample uint32, box string, err error) error {
	return &CorruptionError{
		Code: "truncated-mp4-" + box, TrackID: track, Sample: sample, Recoverable: false,
		Message: fmt.Sprintf("read %s: %v", box, err),
	}
}
