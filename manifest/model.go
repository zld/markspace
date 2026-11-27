package manifest

// KindVideo and KindAudio enumerate the agreed track kinds.
const (
	KindVideo = "video"
	KindAudio = "audio"
)

// SegmentRef is one media segment located on the unified time base.
// StartNS/DurationNS are int64 nanoseconds; URI is a validated relative path.
type SegmentRef struct {
	Index      int    `json:"index"`
	URI        string `json:"uri"`
	StartNS    int64  `json:"start_ns"`
	DurationNS int64  `json:"duration_ns"`
	// Sequence is the media sequence number (HLS) or $Number$ (DASH).
	Sequence int64 `json:"sequence"`
}

// Track is one rendition of a program.
type Track struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	Timescale uint64       `json:"timescale"`
	InitURI   string       `json:"init_uri,omitempty"`
	Segments  []SegmentRef `json:"segments"`
}

// Manifest is the normalized result of parsing either an HLS playlist or MPD.
type Manifest struct {
	ProgramKey string   `json:"program_key"`
	Format     string   `json:"format"` // "hls" | "dash"
	BaseDir    string   `json:"base_dir"`
	DurationNS int64    `json:"duration_ns"`
	Tracks     []*Track `json:"tracks"`
}

// findTrack returns the track with the given id, creating it when create is set.
func (m *Manifest) findTrack(id string, create bool) *Track {
	for _, t := range m.Tracks {
		if t.ID == id {
			return t
		}
	}
	if !create {
		return nil
	}
	t := &Track{ID: id}
	m.Tracks = append(m.Tracks, t)
	return t
}

// validateTimescale enforces the zero-timescale rule shared by both formats.
func validateTimescale(ts uint64, asset, track, stage string, line int) error {
	if ts == 0 {
		return &Error{Code: ErrZeroTimescale, Asset: asset, Track: track, Stage: stage,
			Line: line, Message: "timescale must be greater than zero"}
	}
	return nil
}
