// Package demux defines the common media model, explicit track mapping, and
// format routers for MPEG-TS and MP4 inputs.
package demux

import "medpipe/internal/fault"

// Sample is one retained compressed media sample. Its Data is never decoded
// or re-encoded by medpipe.
type Sample struct {
	Index      int    `json:"index"`
	PID        uint16 `json:"pid"`
	PTSRaw     uint64 `json:"pts_raw,omitempty"`
	PTSMS      int64  `json:"pts_ms"`
	DurationMS int64  `json:"duration_ms"`
	Keyframe   bool   `json:"keyframe"`
	Offset     int64  `json:"offset"`
	Data       []byte `json:"-"`
	CRC32      uint32 `json:"crc32,omitempty"`
	HasCRC     bool   `json:"has_crc,omitempty"`
}

// Track is a demultiplexed elementary track.
type Track struct {
	ID         uint32   `json:"id"`
	PID        uint16   `json:"pid,omitempty"`
	Kind       string   `json:"kind"`
	Codec      string   `json:"codec"`
	Timescale  uint32   `json:"timescale"`
	Width      uint16   `json:"width,omitempty"`
	Height     uint16   `json:"height,omitempty"`
	SampleRate uint32   `json:"sample_rate,omitempty"`
	Channels   uint16   `json:"channels,omitempty"`
	Samples    []Sample `json:"-"`
}

// Media is a format-independent collection of tracks and observations.
type Media struct {
	Format string        `json:"format"`
	Tracks []*Track      `json:"tracks"`
	Events []fault.Event `json:"events,omitempty"`
}

// MapRule explicitly maps one source track to an output MP4 track ID/index.
// A zero ToIndex means the rule selects the source but lets the router assign
// the next output index.
type MapRule struct {
	FromPID uint16 `json:"from_pid,omitempty"`
	FromID  uint32 `json:"from_id,omitempty"`
	Codec   string `json:"codec,omitempty"`
	Kind    string `json:"kind,omitempty"`
	ToIndex int    `json:"to_index"`
	Enabled bool   `json:"enabled"`
}

// Mapping is a resolved source-to-output relationship.
type Mapping struct {
	SourceIndex int
	OutputIndex int
	Track       *Track
	Rule        MapRule
}

// HasPID reports whether the rule matches a TS PID.
func (r MapRule) matchesPID(pid uint16) bool {
	return r.FromPID == pid || (r.FromPID == 0 && r.FromID == 0)
}

// SelectTracks applies explicit mapping rules. When rules is empty it maps all
// supported tracks in their source order. Unknown codecs are rejected.
func SelectTracks(media *Media, rules []MapRule) ([]Mapping, *fault.Error) {
	if media == nil {
		return nil, fault.New(fault.ErrInternal, "demux.map", 0, "nil media")
	}
	for _, t := range media.Tracks {
		if t.Codec == "unknown" || t.Codec == "" {
			return nil, fault.New(fault.ErrUnknownCodec, "demux.map", 0,
				"refusing to mux track with unknown/unsupported codec").WithPID(t.PID)
		}
	}
	if len(rules) == 0 {
		out := make([]Mapping, 0, len(media.Tracks))
		for i, t := range media.Tracks {
			out = append(out, Mapping{SourceIndex: i, OutputIndex: i + 1, Track: t, Rule: MapRule{Enabled: true}})
		}
		return out, nil
	}

	var out []Mapping
	used := map[int]bool{}
	for _, rule := range rules {
		if !rule.Enabled && rule != (MapRule{}) {
			continue
		}
		found := -1
		for i, t := range media.Tracks {
			if used[i] {
				continue
			}
			if rule.FromPID != 0 && t.PID != rule.FromPID {
				continue
			}
			if rule.FromID != 0 && t.ID != rule.FromID {
				continue
			}
			if rule.Codec != "" && t.Codec != rule.Codec {
				continue
			}
			if rule.Kind != "" && t.Kind != rule.Kind {
				continue
			}
			found = i
			break
		}
		if found < 0 {
			return nil, fault.New(fault.ErrUnmappableTrack, "demux.map", 0,
				"mapping rule did not match any supported track")
		}
		used[found] = true
		outputIndex := rule.ToIndex
		if outputIndex == 0 {
			outputIndex = len(out) + 1
		}
		out = append(out, Mapping{SourceIndex: found, OutputIndex: outputIndex, Track: media.Tracks[found], Rule: rule})
	}
	return out, nil
}
