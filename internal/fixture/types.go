package fixture

// stblOption lets damaged variants suppress or alter individual tables.
type stblOption struct {
	omitStco  bool
	omitStsc  bool
	omitStts  bool
	shiftStco int64 // add to every chunk offset (out-of-range variant)
	badStsc   bool  // first_chunk beyond chunk count
}

// DefaultStblOption returns the intact-table configuration.
func DefaultStblOption() stblOption { return stblOption{} }

// TrackSpec describes one synthetic track to generate.
type TrackSpec struct {
	ID           int
	Kind         string // model.KindVideo / model.KindAudio
	Timescale    int64
	SampleCount  int
	SampleSize   int // bytes per sample in mdat
	Delta        int64
	Width        uint32
	Height       uint32
	StartTick    int64 // non-zero composition origin for some fixtures
	StblOption   stblOption
	FrameSamples int // video samples per chunk; audio always uses 1
}

// MediaSpec is the full synthetic container description.
type MediaSpec struct {
	Seed         int64
	Tracks       []TrackSpec
	FrameSamples int // samples per video frame chunk; audio uses 1 per chunk
}

// trackPlan holds the laid-out mdat geometry for one track.
type trackPlan struct {
	spec    TrackSpec
	offsets []int64 // absolute mdat-inclusive file offsets of each sample
	sizes   []int64
}

// Layout records the byte geometry of a generated container so damaged
// variants can corrupt precise ranges.
type Layout struct {
	MdatOffset  int64
	MdatPayload int64
	Tracks      map[int]*TrackLayout
}

// TrackLayout exposes one track's sample geometry.
type TrackLayout struct {
	TrackID      int
	SampleOffset []int64
	SampleSize   []int64
	ChunkOffset  []int64
}

// DefaultSpec builds the standard 2-video + 1-audio archive used by the
// acceptance demo. frames controls the length of both video tracks.
func DefaultSpec(seed int64, frames int) MediaSpec {
	if frames < 8 {
		frames = 8
	}
	return MediaSpec{
		Seed:         seed,
		FrameSamples: 4,
		Tracks: []TrackSpec{
			{ID: 1, Kind: "vide", Timescale: 24000, SampleCount: frames, SampleSize: 256,
				Delta: 1001, Width: 64, Height: 48},
			{ID: 2, Kind: "vide", Timescale: 24000, SampleCount: frames, SampleSize: 192,
				Delta: 1001, Width: 64, Height: 48, StartTick: 2002},
			{ID: 3, Kind: "soun", Timescale: 48000, SampleCount: frames * 2, SampleSize: 96,
				Delta: 1024},
		},
	}
}
