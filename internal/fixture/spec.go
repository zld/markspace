// Package fixture builds deterministic CMAF/fMP4 sample streams in memory and
// injects the faults used by tests and the demo. Nothing is read from disk:
// every byte is derived from the spec seed.
package fixture

// TrackSpec describes one synthetic track.
type TrackSpec struct {
	ID             uint32 `json:"track_id"`
	Kind           string `json:"kind"` // "video" or "audio"
	Timescale      uint32 `json:"timescale"`
	SampleDuration uint32 `json:"sample_duration"`
	Width          uint32 `json:"width,omitempty"`
	Height         uint32 `json:"height,omitempty"`
}

// Spec describes a complete stream.
type Spec struct {
	Seed               int64       `json:"seed"`
	Tracks             []TrackSpec `json:"tracks"`
	Fragments          int         `json:"fragments"`
	SamplesPerFragment int         `json:"samples_per_fragment"`
	TimelineTimescale  uint32      `json:"timeline_timescale"`
}

// DefaultSpec returns a spec with the requested track and fragment counts.
// Tracks beyond the two built-in definitions alternate between the video and
// audio layouts with fresh track ids.
func DefaultSpec(seed int64, tracks, fragments int) Spec {
	all := []TrackSpec{
		{ID: 1, Kind: "video", Timescale: 90000, SampleDuration: 3000, Width: 640, Height: 360},
		{ID: 2, Kind: "audio", Timescale: 48000, SampleDuration: 1024},
	}
	if tracks < 1 {
		tracks = 1
	}
	for i := len(all); i < tracks; i++ {
		t := all[i%2]
		t.ID = uint32(i + 1)
		all = append(all, t)
	}
	return Spec{
		Seed:               seed,
		Tracks:             append([]TrackSpec(nil), all[:tracks]...),
		Fragments:          fragments,
		SamplesPerFragment: 2,
		TimelineTimescale:  90000,
	}
}

// Validate reports whether the spec can be serialized.
func (s Spec) Validate() error {
	if s.Fragments <= 0 {
		return errSpec("fragments must be positive")
	}
	if len(s.Tracks) == 0 {
		return errSpec("at least one track is required")
	}
	if s.TimelineTimescale == 0 {
		return errSpec("timeline timescale must be positive")
	}
	if s.SamplesPerFragment <= 0 {
		return errSpec("samples_per_fragment must be positive")
	}
	seen := make(map[uint32]bool, len(s.Tracks))
	for _, t := range s.Tracks {
		if t.ID == 0 || t.Timescale == 0 || t.SampleDuration == 0 {
			return errSpec("track id, timescale and sample duration must be positive")
		}
		if seen[t.ID] {
			return errSpec("duplicate track id")
		}
		seen[t.ID] = true
	}
	return nil
}

type specError string

func (e specError) Error() string { return "fixture: " + string(e) }

func errSpec(msg string) error { return specError(msg) }

// SampleInfo locates one sample in the serialized stream.
type SampleInfo struct {
	Index      int    `json:"index"`
	Offset     int64  `json:"offset"`
	Size       int    `json:"size"`
	PayloadSHA string `json:"payload_sha256"`
}

// FragmentInfo locates one moof+mdat pair.
type FragmentInfo struct {
	Index      int          `json:"index"`
	TrackID    uint32       `json:"track_id"`
	Sequence   uint32       `json:"sequence"`
	MoofOffset int64        `json:"moof_offset"`
	MoofSize   int64        `json:"moof_size"`
	MdatOffset int64        `json:"mdat_offset"`
	MdatSize   int64        `json:"mdat_size"`
	TfdtOffset int64        `json:"tfdt_offset"`
	Tfdt       int64        `json:"tfdt"`
	TrunOffset int64        `json:"trun_offset"`
	Samples    []SampleInfo `json:"samples"`
}

// Stream is a serialized fixture plus its byte map.
type Stream struct {
	Spec       Spec           `json:"spec"`
	Data       []byte         `json:"-"`
	InitSize   int64          `json:"init_size"`
	SidxOffset int64          `json:"sidx_offset"`
	SidxSize   int64          `json:"sidx_size"`
	SidxRefs   int            `json:"sidx_refs"`
	Fragments  []FragmentInfo `json:"fragments"`
	Digest     string         `json:"sha256"`
}
