package media

import "time"

// SegmentConfig controls keyframe-aligned and duration-limited cutting.
type SegmentConfig struct {
	// Duration is the preferred keyframe-aligned segment duration.
	Duration time.Duration
	// MaxDuration forces a boundary even if no keyframe arrived.
	MaxDuration time.Duration
}

// Segment is the manifest record for one finalized interval.
type Segment struct {
	Index          int    `json:"index"`
	StartNS        int64  `json:"start_ns"`
	EndNS          int64  `json:"end_ns"`
	DurationNS     int64  `json:"duration_ns"`
	StartSequence  uint16 `json:"start_sequence"`
	EndSequence    uint16 `json:"end_sequence"`
	StartExtSeq    uint64 `json:"start_ext_sequence"`
	EndExtSeq      uint64 `json:"end_ext_sequence"`
	FrameCount     int    `json:"frame_count"`
	StartsKeyFrame bool   `json:"starts_keyframe"`
	Fingerprint    string `json:"fingerprint"`
	File           string `json:"file,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	ByteSize       int    `json:"byte_size,omitempty"`
	DuplicateOf    int    `json:"duplicate_of,omitempty"`
}

// BuiltSegment is a finalized segment plus the valid frames needed to mux it.
type BuiltSegment struct {
	Info   Segment
	Frames []Frame
}

type segmentBuilder struct {
	info   Segment
	fp     *Fingerprint
	frames []Frame
}

// Segmenter cuts only at valid frame boundaries. Truncated/corrupt frames are
// never passed to Accept, so they cannot straddle or enter an output segment.
type Segmenter struct {
	cfg     SegmentConfig
	index   int
	current *segmentBuilder
}

func NewSegmenter(cfg SegmentConfig) *Segmenter {
	if cfg.Duration == 0 {
		cfg.Duration = 4 * time.Second
	}
	if cfg.MaxDuration == 0 {
		cfg.MaxDuration = cfg.Duration + time.Second
	}
	return &Segmenter{cfg: cfg}
}

// Accept adds a frame and returns a non-nil BuiltSegment when the frame starts
// after a boundary. The returned segment does not contain f; f starts the next.
func (s *Segmenter) Accept(f Frame) (*BuiltSegment, error) {
	if s.current == nil {
		s.start(f)
		return nil, nil
	}

	start := time.Duration(s.current.info.StartNS)
	elapsed := f.PTS - start
	forceDuration := s.cfg.MaxDuration > 0 && elapsed >= s.cfg.MaxDuration
	keyAlignedDuration := f.Key && s.cfg.Duration > 0 && elapsed >= s.cfg.Duration
	if forceDuration || keyAlignedDuration {
		built := s.close()
		s.start(f)
		return built, nil
	}

	s.add(f)
	return nil, nil
}

// Flush finalizes the trailing segment.
func (s *Segmenter) Flush() *BuiltSegment {
	if s.current == nil {
		return nil
	}
	b := s.close()
	s.current = nil
	return b
}

func (s *Segmenter) start(f Frame) {
	s.index++
	fp := NewFingerprint()
	fp.Add(f)
	s.current = &segmentBuilder{
		info: Segment{
			Index:          s.index,
			StartNS:        int64(f.PTS),
			EndNS:          int64(f.End()),
			DurationNS:     int64(f.Duration),
			StartSequence:  f.Sequence,
			EndSequence:    f.Sequence,
			StartExtSeq:    f.ExtSequence,
			EndExtSeq:      f.ExtSequence,
			FrameCount:     1,
			StartsKeyFrame: f.Key,
			Fingerprint:    "",
		},
		fp:     fp,
		frames: []Frame{f},
	}
}

func (s *Segmenter) add(f Frame) {
	b := s.current
	b.fp.Add(f)
	b.frames = append(b.frames, f)
	b.info.EndNS = int64(f.End())
	b.info.DurationNS = b.info.EndNS - b.info.StartNS
	b.info.EndSequence = f.Sequence
	b.info.EndExtSeq = f.ExtSequence
	b.info.FrameCount++
}

func (s *Segmenter) close() *BuiltSegment {
	b := s.current
	if b == nil {
		return nil
	}
	b.info.Fingerprint = b.fp.Sum()
	frames := append([]Frame(nil), b.frames...)
	return &BuiltSegment{Info: b.info, Frames: frames}
}

// SegmenterState is sufficient to resume a segment that has not yet closed.
type SegmenterState struct {
	NextIndex int                `json:"next_index"`
	Current   *SegmentBuildState `json:"current,omitempty"`
}

type SegmentBuildState struct {
	Info   Segment          `json:"info"`
	FP     FingerprintState `json:"fp"`
	Frames []FrameState     `json:"frames"`
}

func (s *Segmenter) State() SegmenterState {
	st := SegmenterState{NextIndex: s.index}
	if s.current != nil {
		b := s.current
		cur := &SegmentBuildState{Info: b.info, FP: b.fp.State()}
		for _, f := range b.frames {
			cur.Frames = append(cur.Frames, f.state())
		}
		st.Current = cur
	}
	return st
}

func (s *Segmenter) Restore(st SegmenterState) {
	s.index = st.NextIndex
	s.current = nil
	if st.Current == nil {
		return
	}
	fp := NewFingerprint()
	fp.Restore(st.Current.FP)
	b := &segmentBuilder{info: st.Current.Info, fp: fp}
	for _, fs := range st.Current.Frames {
		f := frameFromState(fs)
		b.frames = append(b.frames, f)
	}
	s.current = b
}
