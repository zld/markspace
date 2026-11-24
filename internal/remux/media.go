package remux

import "mediapipeline/internal/demux"

// media is the ordered payload and per-track chunk offsets.
type media struct {
	payload []byte
	chunks  map[uint32][]uint32
	sizes   map[uint32][]uint32
	order   []uint32
	// trackByID keeps the sample list in step with the sizes so the sync flag
	// and timing tables can be rebuilt without re-deriving them.
	trackByID map[uint32]demux.Track
}

// relocate rewrites chunk offsets now that the mdat payload position is known.
