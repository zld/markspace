package model

// MediaKind identifies a track's media type.
type MediaKind string

const (
	KindVideo MediaKind = "video"
	KindAudio MediaKind = "audio"
	KindHint  MediaKind = "hint"
	KindOther MediaKind = "other"
)

// Policy is the explicit handling strategy for conflicting metadata. The
// pipeline must never silently guess: every conflict gets one of these.
type Policy string

const (
	// PolicyNormalize rewrites metadata to the declared delivery target.
	PolicyNormalize Policy = "normalize"
	// PolicyPreserve keeps the source metadata untouched and annotates it.
	PolicyPreserve Policy = "preserve"
	// PolicyIsolate drops the affected range/track from the remux output.
	PolicyIsolate Policy = "isolate"
)

// VideoFormat carries the delivery-relevant video metadata.
type VideoFormat struct {
	Codec             string `json:"codec"`
	Width             uint16 `json:"width"`
	Height            uint16 `json:"height"`
	ColourType        string `json:"colour_type"`
	ColourPrimaries   uint16 `json:"colour_primaries"`
	Transfer          uint16 `json:"transfer_characteristics"`
	Matrix            uint16 `json:"matrix_coefficients"`
	FullRange         bool   `json:"full_range"`
	HasColorMetadata  bool   `json:"has_color_metadata"`
}

// AudioFormat carries the delivery-relevant audio metadata.
type AudioFormat struct {
	Codec        string `json:"codec"`
	ChannelCount uint16 `json:"channel_count"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	SampleRate   uint32 `json:"sample_rate"` // Hz
	SampleSize   uint16 `json:"sample_size"`
}

// Sample is one entry on the unified timeline. All times are Rat seconds.
type Sample struct {
	Index       int  `json:"index"`
	DecodeTime  Rat  `json:"-"`
	Composition Rat  `json:"-"`
	Duration    Rat  `json:"-"`
	Size        uint32 `json:"size"`
	IsKeyframe  bool `json:"is_keyframe"`
	// ChunkIndex locates the sample's payload inside the mdat box.
	ChunkIndex int    `json:"chunk_index"`
	Offset     uint64 `json:"offset"`
}

// SampleReport is the JSON-friendly projection of a Sample. Rational times
// are rendered as exact fractions to avoid float drift in evidence.
type SampleReport struct {
	Index      int    `json:"index"`
	DecodeTime string `json:"decode_time"`
	Composition string `json:"composition_time"`
	Duration   string `json:"duration"`
	Size       uint32 `json:"size"`
	IsKeyframe bool   `json:"is_keyframe"`
}

// Track is one node of the track graph.
type Track struct {
	ID        uint32    `json:"id"`
	Kind      MediaKind `json:"kind"`
	Timescale uint32    `json:"timescale"`
	Handler   string    `json:"handler"`
	// StartTime / EndTime are on the unified (seconds) timeline.
	StartTime Rat    `json:"-"`
	EndTime   Rat    `json:"-"`
	Samples   []*Sample `json:"-"`
	Video     *VideoFormat `json:"video,omitempty"`
	Audio     *AudioFormat `json:"audio,omitempty"`
	// DependsOn lists track IDs this track needs before it can be recovered
	// or remuxed (e.g. audio alignment depends on video boundary recovery).
	DependsOn []uint32 `json:"depends_on"`
	// KeyframeInterval is the delivery maximum between adjacent keyframes,
	// in seconds. Zero disables the check.
	KeyframeInterval Rat `json:"-"`
	// KeyframeIndexPresent mirrors whether stss existed in the source.
	KeyframeIndexPresent bool `json:"keyframe_index_present"`
}

// SampleCount is a nil-safe accessor.
func (t *Track) SampleCount() int {
	if t == nil {
		return 0
	}
	return len(t.Samples)
}

// Coverage returns the track's full timeline span as a half-open range.
func (t *Track) Coverage() (Range, error) {
	return NewRange(t.StartTime, t.EndTime)
}

// Report converts samples into deterministic JSON records.
func (t *Track) Report() []SampleReport {
	out := make([]SampleReport, 0, len(t.Samples))
	for _, s := range t.Samples {
		out = append(out, SampleReport{
			Index:       s.Index,
			DecodeTime:  s.DecodeTime.String(),
			Composition: s.Composition.String(),
			Duration:    s.Duration.String(),
			Size:        s.Size,
			IsKeyframe:  s.IsKeyframe,
		})
	}
	return out
}

// Graph is the explicit track dependency graph produced by the probe.
type Graph struct {
	Tracks   map[uint32]*Track
	order    []uint32
}

// NewGraph validates the node count against the delivery limit.
func NewGraph(tracks []*Track) (*Graph, error) {
	if len(tracks) > MaxTracks {
		return nil, &LimitError{What: "tracks", Got: len(tracks), Max: MaxTracks}
	}
	g := &Graph{Tracks: make(map[uint32]*Track, len(tracks))}
	for _, t := range tracks {
		if _, dup := g.Tracks[t.ID]; dup {
			return nil, &DuplicateTrackError{ID: t.ID}
		}
		g.Tracks[t.ID] = t
		g.order = append(g.order, t.ID)
	}
	return g, nil
}

// IDs returns tracks in source order.
func (g *Graph) IDs() []uint32 {
	out := make([]uint32, len(g.order))
	copy(out, g.order)
	return out
}

// LimitError is returned when a hard delivery limit is exceeded.
type LimitError struct {
	What string
	Got  int
	Max  int
}

func (e *LimitError) Error() string {
	return fmtLimit(e.What, e.Got, e.Max)
}

// DuplicateTrackError flags two tracks with the same track id.
type DuplicateTrackError struct{ ID uint32 }

func (e *DuplicateTrackError) Error() string {
	return "duplicate track id " + u32(e.ID)
}
