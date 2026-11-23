package probe

import (
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
	"github.com/broadcast/mediad/internal/model"
)

// ProbeConfig carries delivery thresholds and conflict policies into parsing.
type ProbeConfig struct {
	// MaxKeyframeGap is the maximum allowed gap in seconds between adjacent
	// keyframes on video tracks.
	MaxKeyframeGap model.Rat
	// Policies select explicit handling when metadata contradicts itself.
	ChannelPolicy model.Policy
	ColorPolicy   model.Policy
}

// DefaultConfig returns sensible delivery defaults: 2 s keyframe spacing and
// normalization for metadata conflicts.
func DefaultConfig() ProbeConfig {
	return ProbeConfig{
		MaxKeyframeGap: model.MustRat(2, 1),
		ChannelPolicy:  model.PolicyNormalize,
		ColorPolicy:    model.PolicyNormalize,
	}
}

// Probe parses an MP4/MOV seekable stream into a graph and timeline. All
// structural defects are pushed into the shared ErrorSet; only unrecoverable
// parser-level failures (no moov, corrupt root boxes) return a Go error.
func Probe(r io.ReadSeeker, fileSize int64, cfg ProbeConfig, errs *model.ErrorSet) (*model.Graph, *ContainerMeta, error) {
	scan, err := ScanBoxes(r, fileSize, errs)
	if err != nil {
		return nil, nil, err
	}
	if _, ok := scan.FindBox("moov"); !ok {
		return nil, nil, errors.New("no moov box found: not an ISO BMFF/MOV file")
	}

	var (
		moovInfo *mp4.BoxInfo
		traks    []*mp4.BoxInfo
	)
	if _, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch {
		case h.BoxInfo.Type == mp4.BoxTypeMoov() && len(h.Path) == 1:
			info := h.BoxInfo
			moovInfo = &info
			return h.Expand()
		case h.BoxInfo.Type == mp4.BoxTypeTrak() && len(h.Path) == 2:
			info := h.BoxInfo
			traks = append(traks, &info)
			return nil, nil // no need to descend generically
		case len(h.Path) <= 1:
			return h.Expand()
		default:
			return nil, nil
		}
	}); err != nil {
		return nil, nil, fmt.Errorf("reading box structure: %w", err)
	}
	if moovInfo == nil || len(traks) == 0 {
		return nil, nil, errors.New("moov contains no parseable tracks")
	}
	if len(traks) > model.MaxTracks {
		return nil, nil, &model.LimitError{What: "tracks", Got: len(traks), Max: model.MaxTracks}
	}

	meta := &ContainerMeta{}
	if mv, err := extractOne(r, moovInfo, mp4.BoxTypeMvhd()); err == nil {
		if mvhd, ok := mv.Payload.(*mp4.Mvhd); ok {
			meta.MovieTimescale = mvhd.Timescale
			meta.MovieDuration = mvhd.GetDuration()
		}
	}

	var tracks []*model.Track
	for _, tb := range traks {
		t, perr := parseTrack(r, tb, cfg, errs)
		if perr != nil {
			errs.Add(model.MediaError{
				Kind:        model.KindTruncatedBox,
				Severity:    model.SeverityError,
				SampleIndex: -1,
				Detail:      fmt.Sprintf("track parse failed: %v", perr),
			})
			continue
		}
		tracks = append(tracks, t)
	}
	g, err := model.NewGraph(tracks)
	if err != nil {
		return nil, nil, err
	}
	return g, meta, nil
}

// ContainerMeta captures movie-level metadata used by reports.
type ContainerMeta struct {
	MovieTimescale uint32 `json:"movie_timescale"`
	MovieDuration  uint64 `json:"movie_duration"`
}

func extractOne(r io.ReadSeeker, parent *mp4.BoxInfo, path ...mp4.BoxType) (*mp4.BoxInfoWithPayload, error) {
	got, err := mp4.ExtractBoxesWithPayload(r, parent, []mp4.BoxPath{path})
	if err != nil {
		return nil, err
	}
	if len(got) == 0 {
		return nil, mp4.ErrBoxInfoNotFound
	}
	return got[0], nil
}
