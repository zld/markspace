package stream

import (
	"mediainspect/internal/media"
	"mediainspect/internal/normalize"
)

// OutputFrame is one executed frame on the unified timebase.
type OutputFrame struct {
	Frame       *media.Frame
	TimestampMs float64 // unified media time in milliseconds
	Damaged     bool    // inside a corrupt range; excluded from valid output
	Errors      []*media.FrameError
	Anomalies   []string
}

// ExecStats aggregates execution counters shared with the metrics stage.
type ExecStats struct {
	InputFrames   int
	OutputFrames  int
	ValidFrames   int
	DamagedFrames int
	Duplicates    int
	LateDropped   int
	LostFrames    int
	Recovered     int
	AnomalyCount  int
	ErrorsByClass map[media.ErrorClass]int
}

// Executor consumes frames (any order, with duplicates and loss), restores
// order, unwraps timestamps onto the unified timebase, localizes damage and
// normalizes audio/color metadata.
type Executor struct {
	reorder          *Reorderer
	damage           DamageTracker
	target           normalize.AudioTarget
	keyframeInterval int
	frameTicks       int64

	lastTs          int64
	lastSeq         int64
	lastKeyframeSeq int64
	started         bool

	Stats ExecStats
}

func NewExecutor(window, keyframeInterval int, frameTicks int64, target normalize.AudioTarget) *Executor {
	return &Executor{
		reorder:          NewReorderer(window),
		target:           target,
		keyframeInterval: keyframeInterval,
		frameTicks:       frameTicks,
		Stats:            ExecStats{ErrorsByClass: map[media.ErrorClass]int{}},
	}
}

// Push ingests one frame and returns executed output frames.
func (e *Executor) Push(f *media.Frame) []OutputFrame {
	e.Stats.InputFrames++
	return e.processAll(e.reorder.Push(f))
}

// Flush drains the reorder window at end of input.
func (e *Executor) Flush() []OutputFrame {
	out := e.processAll(e.reorder.Flush())
	e.damage.Recover() // close any trailing open range
	rs := e.reorder.Stats
	e.Stats.Duplicates, e.Stats.LateDropped = rs.Duplicates, rs.LateDropped
	e.Stats.LostFrames, e.Stats.Recovered = rs.Lost, rs.Recovered
	return out
}

// Ranges returns the closed corrupt ranges.
func (e *Executor) Ranges() []CorruptRange { return e.damage.Ranges }

func (e *Executor) processAll(frames []*media.Frame) []OutputFrame {
	var out []OutputFrame
	for _, f := range frames {
		out = append(out, e.process(f))
	}
	return out
}

func (e *Executor) process(f *media.Frame) OutputFrame {
	ext := int64(f.Seq)
	if e.started {
		ext = media.ExtendSeq(f.Seq, e.lastSeq)
	}
	var delta int64 = 1
	if e.started {
		delta = ext - e.lastSeq
	}

	// Timestamp unwrap by sequence and modulus against the unified timebase.
	expected := int64(f.Timestamp)
	if e.started {
		expected = e.lastTs + delta*e.frameTicks
	}
	unwrapped := media.UnwrapTimestamp(f.Timestamp, expected)
	ms := media.TicksToMs(unwrapped)

	out := OutputFrame{Frame: f, TimestampMs: ms}
	var errs []*media.FrameError
	mkErr := func(class media.ErrorClass, detail string) {
		errs = append(errs, &media.FrameError{Class: class, StreamID: f.StreamID, Seq: f.Seq, Detail: detail})
	}

	damaging := false
	switch {
	case delta > media.SeqModulus/2 || delta < -media.SeqModulus/2:
		// Cannot uniquely place the frame on the timeline: mark damaged and
		// stop valid output for the segment until the next valid keyframe.
		mkErr(media.ClassTimestamp, media.ErrAmbiguousTimestamp.Error())
		e.damage.Begin("ambiguous_timestamp", f.Seq, ms)
		damaging = true
	case e.started && abs64(unwrapped-expected) > e.frameTicks/2:
		mkErr(media.ClassTimestamp, "timestamp deviates from sequence-derived expectation")
		e.damage.Begin("timestamp_anomaly", f.Seq, ms)
		damaging = true
	}
	if delta > 1 && e.started {
		e.damage.Begin("frame_loss", f.Seq, ms)
		damaging = true
	}
	if !f.ChecksumOK() {
		mkErr(media.ClassChecksum, "payload checksum mismatch")
		e.damage.Begin("checksum", f.Seq, ms)
		damaging = true
	}
	if f.Truncated() {
		mkErr(media.ClassTruncation, "frame truncated")
		e.damage.Begin("truncation", f.Seq, ms)
		damaging = true
	}
	if e.started && ext-e.lastKeyframeSeq > int64(e.keyframeInterval*3/2) {
		e.damage.Begin("missing_keyframe", f.Seq, ms)
		damaging = true
	}

	// Metadata normalization runs on every frame; failures are per-frame
	// errors and never silently converted.
	audioRes, audioErrs := normalize.Audio(f.Audio, e.target, f.StreamID, f.Seq)
	colorRes, colorErrs := normalize.Color(f.Color, f.StreamID, f.Seq)
	errs = append(errs, audioErrs...)
	errs = append(errs, colorErrs...)
	out.Anomalies = colorRes.Anomalies
	_ = audioRes

	// A structurally valid keyframe ends the damaged segment; metadata errors
	// do not block video decode recovery. Output resumes at this keyframe.
	validKey := f.Keyframe && !damaging
	if validKey {
		e.damage.Recover()
		e.lastKeyframeSeq = ext
	}
	if !e.started {
		e.lastKeyframeSeq = ext
	}

	if e.damage.InDamage() {
		out.Damaged = true
		e.damage.Extend(f.Seq, ms)
		e.Stats.DamagedFrames++
	} else {
		e.Stats.ValidFrames++
	}

	out.Errors = errs
	for _, fe := range errs {
		e.Stats.ErrorsByClass[fe.Class]++
	}
	e.Stats.AnomalyCount += len(out.Anomalies)
	e.Stats.OutputFrames++
	e.lastTs, e.lastSeq = unwrapped, ext
	e.started = true
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
