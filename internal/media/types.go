package media

// Sample is one media sample and its decode delta.
type Sample struct {
	Data  []byte
	Delta uint32
	Sync  bool
}

// Track is a deterministic video or audio track description.
type Track struct {
	ID         uint32
	Kind       string // "vide" or "soun"
	Timescale  uint32
	Width      uint16
	Height     uint16
	Channels   uint16
	SampleRate uint32
	Samples    []Sample
	offsets    []uint32
}
