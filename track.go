package mediakernel

// Track describes one Matroska track entry.
type Track struct {
	Number          uint64  `json:"number"`
	Type            uint64  `json:"type"`
	Codec           string  `json:"codec"`
	DefaultDuration int64   `json:"defaultDurationNs"`
	TimecodeScale   float64 `json:"timecodeScale"` // track factor, default 1.0
}

// ErrTrackUnknown reports a block referencing an undeclared track number.
const ErrTrackUnknown Code = "TRACK_UNKNOWN"

// TrackRegistry holds the tracks declared in a Tracks element.
type TrackRegistry struct {
	Tracks map[uint64]*Track
}

func NewTrackRegistry() *TrackRegistry {
	return &TrackRegistry{Tracks: make(map[uint64]*Track)}
}

// GetOrCreate returns the track for number, creating a default entry.
func (r *TrackRegistry) GetOrCreate(number uint64) *Track {
	t, ok := r.Tracks[number]
	if !ok {
		t = &Track{Number: number, TimecodeScale: 1.0}
		r.Tracks[number] = t
	}
	return t
}
