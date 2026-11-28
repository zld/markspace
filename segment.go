package mediakernel

// BlockRef is a block's position inside a committed segment.
type BlockRef struct {
	Track  uint64 `json:"track"`
	Ns     int64  `json:"ns"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
}

// Segment is a half-open time interval [Start, End); every block belongs to
// exactly one segment.
type Segment struct {
	Index  int64      `json:"index"`
	Start  int64      `json:"startNs"`
	End    int64      `json:"endNs"`
	Blocks []BlockRef `json:"blocks"`
}

// Segmenter assigns blocks to fixed-duration half-open segments.
type Segmenter struct {
	Dur int64 // segment duration in nanoseconds
}

func NewSegmenter(durNs int64) *Segmenter {
	if durNs <= 0 {
		durNs = 1_000_000_000
	}
	return &Segmenter{Dur: durNs}
}

// Index returns the segment index for a timestamp (floor division).
func (s *Segmenter) Index(ns int64) int64 {
	i := ns / s.Dur
	if ns < 0 && ns%s.Dur != 0 {
		i--
	}
	return i
}

// Range returns the half-open bounds of segment i.
func (s *Segmenter) Range(i int64) (start, end int64) {
	return i * s.Dur, (i + 1) * s.Dur
}
