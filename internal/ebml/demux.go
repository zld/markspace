package ebml

// Demuxer consumes walker events and produces blocks, metadata and errors.
//
// It emits blocks as they are confirmed, so a truncated tail still leaves a
// usable prefix for indexing.
type Demuxer struct {
	w    *Walker
	info FileInfo

	inInfo    int
	inTracks  int
	inTrack   int
	inCluster int

	curTrack   TrackInfo
	clusterTC  uint32
	clusterOff int64

	// OnBlock receives every confirmed block.
	OnBlock func(Block)

	// Count is the number of blocks emitted.
	Count int
}

// NewDemuxer wraps a walker.
func NewDemuxer(w *Walker) *Demuxer { return &Demuxer{w: w} }

// Info returns metadata discovered so far.
func (d *Demuxer) Info() FileInfo { return d.info }

// trackType reports a declared track's type.
func (d *Demuxer) trackType(num uint64) (uint64, bool) {
	for _, t := range d.info.Tracks {
		if t.Number == num {
			return t.Type, true
		}
	}
	return 0, false
}

func (d *Demuxer) emit(b Block) {
	d.Count++
	if d.OnBlock != nil {
		d.OnBlock(b)
	}
}
