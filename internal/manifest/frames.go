// Package manifest parses delivered frame-sequence manifests into the
// canonical model and maps every timestamp onto the shared 90 kHz timeline.
// Parsing covers non-zero start offsets, per-frame interval jitter and
// boundary rounding; frame payload integrity is verified separately in the
// verify package.
package manifest

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"

	"github.com/pion/rtp"

	"mediapipe/internal/clock"
	"mediapipe/internal/model"
)

// frameDTO is the on-disk JSON shape of one delivered frame.
type frameDTO struct {
	Index        int            `json:"index"`
	Name         string         `json:"name"`
	Length       int64          `json:"length"`
	SHA256       string         `json:"sha256"`
	TimeUnits    int64          `json:"time_units"`
	TimeRate     int64          `json:"time_rate"`
	Color        model.ColorMeta `json:"color"`
	Seq          uint16         `json:"rtp_seq"`
	RTPTimestamp uint32         `json:"rtp_timestamp"`
}

type trackDTO struct {
	ID               string      `json:"id"`
	Kind             model.Kind  `json:"kind"`
	FPS              int64       `json:"fps"`
	SampleRate       int64       `json:"sample_rate"`
	Channels         int         `json:"channels"`
	Layout           string      `json:"layout"`
	StartOffsetUnits int64       `json:"start_offset_units"`
	StartOffsetRate  int64       `json:"start_offset_rate"`
	LoudnessLUFS     float64     `json:"loudness_lufs"`
	Frames           []frameDTO  `json:"frames"`
}

type fileDTO struct {
	Tracks []trackDTO `json:"tracks"`
}

// MappedFrame pairs a canonical frame with its resolved position on the
// shared 90 kHz timeline.
type MappedFrame struct {
	Frame  model.Frame
	At90k  clock.Ticks
	NextAt clock.Ticks // start of the following frame; zero for the last frame
}

// MappedTrack carries a track plus its resolved timeline mapping.
type MappedTrack struct {
	Track        *model.Track
	Start90k     clock.Ticks
	Frames       []MappedFrame
}

// Parse reads and validates a manifest JSON stream, mapping every frame onto
// the shared timeline. Conflicts on the same tick are ordered by track id
// then frame index (the canonical OrderKey rule).
func Parse(r io.Reader) ([]*MappedTrack, error) {
	var dto fileDTO
	dec := json.NewDecoder(r)
	if err := dec.Decode(&dto); err != nil {
		return nil, model.NewError(model.ErrInvalidConfig, "", 0, 0, "manifest decode: "+err.Error())
	}
	if len(dto.Tracks) == 0 {
		return nil, model.NewError(model.ErrInvalidConfig, "", 0, 0, "manifest has no tracks")
	}

	out := make([]*MappedTrack, 0, len(dto.Tracks))
	tl := &model.Timeline{}
	for _, td := range dto.Tracks {
		mt, err := convertTrack(td)
		if err != nil {
			return nil, err
		}
		out = append(out, mt)
		tl.Tracks = append(tl.Tracks, mt.Track)
	}
	if err := tl.Validate(); err != nil {
		return nil, model.NewError(model.ErrInvalidConfig, "", 0, 0, err.Error())
	}
	// Stable presentation order regardless of file ordering.
	sort.Slice(out, func(i, j int) bool { return out[i].Track.ID < out[j].Track.ID })
	return out, nil
}

// ParseFile opens and parses a manifest path.
func ParseFile(path string) ([]*MappedTrack, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.ErrIO, "", 0, 0, err.Error())
	}
	defer f.Close()
	return Parse(f)
}

// stableSSRC derives a deterministic SSRC from a track id so repeated runs
// produce identical RTP headers without any randomness.
func stableSSRC(trackID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(trackID))
	return h.Sum32()
}

func convertTrack(td trackDTO) (*MappedTrack, error) {
	tr := &model.Track{
		ID:               td.ID,
		Kind:             td.Kind,
		FPS:              td.FPS,
		SampleRate:       td.SampleRate,
		Channels:         td.Channels,
		Layout:           td.Layout,
		StartOffsetUnits: td.StartOffsetUnits,
		StartOffsetRate:  td.StartOffsetRate,
		LoudnessLUFS:     td.LoudnessLUFS,
	}
	startRate := td.StartOffsetRate
	if startRate == 0 {
		startRate = 1
	}
	start90k := clock.FromSeconds(td.StartOffsetUnits, startRate)

	var prev clock.Ticks
	mapped := make([]MappedFrame, 0, len(td.Frames))
	for i, fd := range td.Frames {
		digest, err := model.ParseHash(fd.SHA256)
		if err != nil {
			return nil, model.NewError(model.ErrInvalidConfig, td.ID, i, 0, err.Error())
		}
		if fd.TimeRate <= 0 {
			return nil, model.NewError(model.ErrInvalidConfig, td.ID, i, 0,
				fmt.Sprintf("frame %d has non-positive time rate", i))
		}
		at := start90k + clock.FromSeconds(fd.TimeUnits, fd.TimeRate)
		if i > 0 && at < prev {
			return nil, model.NewError(model.ErrTimestampBack, td.ID, i, 0,
				fmt.Sprintf("timestamp %d ticks precedes prior %d", at, prev))
		}
		prev = at
		fr := model.Frame{
			TrackID:   td.ID,
			Index:     i,
			Name:      fd.Name,
			Length:    fd.Length,
			SHA256:    digest,
			TimeUnits: fd.TimeUnits,
			TimeRate:  fd.TimeRate,
			Color:     fd.Color,
			RTP: rtp.Header{
				Version:        2,
				SequenceNumber: fd.Seq,
				Timestamp:      fd.RTPTimestamp,
				SSRC:           stableSSRC(td.ID),
			},
		}
		mapped = append(mapped, MappedFrame{Frame: fr, At90k: at})
		tr.Frames = append(tr.Frames, fr)
	}
	for i := 0; i+1 < len(mapped); i++ {
		mapped[i].NextAt = mapped[i+1].At90k
	}
	return &MappedTrack{Track: tr, Start90k: start90k, Frames: mapped}, nil
}
