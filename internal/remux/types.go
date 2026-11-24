package remux

// Sample is one compressed output sample on the common timeline time base.
type Sample struct {
	DTS      int64
	PTS      int64
	Duration int64
	Payload  []byte
	Hash     string
}

// Track configures one output track in mapping order.
type Track struct {
	ID         uint32
	Kind       string
	FourCC     string
	Timescale  uint32
	Width      uint16
	Height     uint16
	Channels   uint16
	SampleRate uint32
	Samples    []Sample
}

// Result summarizes an atomic remux write.
type Result struct {
	Path           string
	SampleCount    int
	HashMismatches int
	OutputBytes    int64
	Timescale      uint32
}
