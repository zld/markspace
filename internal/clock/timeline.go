// Package clock defines the sample-frame time base shared by every
// subsystem. RTP sequence numbers and timestamps are 16/32-bit modular
// counters; all conversions here keep absolute sample-frame counts monotonic
// across wraparound.
package clock

// SeqDelta returns the signed distance from prev to curr for a 16-bit
// counter, handling wraparound per RFC 3550 modular arithmetic.
// A positive result means curr is newer than prev.
func SeqDelta(prev, curr uint16) int32 {
	return int32(int16(curr - prev))
}

// TS32Delta is SeqDelta's 32-bit counterpart for RTP timestamps.
func TS32Delta(prev, curr uint32) int32 {
	return int32(int64(curr) - int64(prev))
}

// Timestamp keeps a monotonic sample-frame cursor alongside the last modular
// RTP values seen on a stream. The zero value is ready for use.
type Timestamp struct {
	lastSeq      uint16
	hasSeq       bool
	seqRollover  uint32 // number of 65536 cycles applied to sequence numbers
	lastTS       uint32
	hasTS        bool
	absTimestamp uint64 // absolute RTP timestamp units since the stream start
	absFrame     uint64 // absolute sample frames (RTP timestamps use 1 unit/frame)
	frameRate    uint32 // samples per second, 0 means "not yet configured"
	framesPerTS  uint32 // RTP timestamp units per sample frame (1 for LPCM)
}

// NewTimestamp creates a timeline for a stream whose RTP timestamps advance
// framesPerTS units per sample frame.
func NewTimestamp(frameRate, framesPerTS uint32) *Timestamp {
	if framesPerTS == 0 {
		framesPerTS = 1
	}
	return &Timestamp{frameRate: frameRate, framesPerTS: framesPerTS}
}

// FrameRate reports the configured sample rate.
func (t *Timestamp) FrameRate() uint32 { return t.frameRate }

// AbsFrame is the total number of sample frames observed, including the
// current packet, counting monotonically across timestamp wraparound.
func (t *Timestamp) AbsFrame() uint64 { return t.absFrame }

// AbsTimestamp reports the unwrapped RTP timestamp in absolute units.
func (t *Timestamp) AbsTimestamp() uint64 { return t.absTimestamp }

// ObserveSeq resolves a modular 16-bit sequence number into an absolute
// sequence index. Duplicate packets report the same index twice; a backward
// jump larger than half the space is treated as a wraparound.
func (t *Timestamp) ObserveSeq(seq uint16) uint64 {
	if !t.hasSeq {
		t.hasSeq = true
		t.lastSeq = seq
		return uint64(seq)
	}
	delta := SeqDelta(t.lastSeq, seq)
	if delta > 0 {
		// Forward step: detect crossing of the 16-bit boundary.
		if uint32(seq) < uint32(t.lastSeq) {
			t.seqRollover++
		}
		t.lastSeq = seq
	}
	return uint64(t.seqRollover)<<16 | uint64(seq)
}

// ObserveTS folds a modular 32-bit RTP timestamp into the absolute timeline.
// Backward movement beyond half the counter space is a wraparound; small
// backward or equal deltas (duplicates) never move the cursor backwards.
func (t *Timestamp) ObserveTS(ts uint32) uint64 {
	if !t.hasTS {
		t.hasTS = true
		t.lastTS = ts
		t.absTimestamp = uint64(ts)
		t.absFrame = uint64(ts) / uint64(t.framesPerTS)
		return t.absFrame
	}
	delta := TS32Delta(t.lastTS, ts)
	if delta > 0 {
		t.absTimestamp += uint64(delta)
		t.absFrame = t.absTimestamp / uint64(t.framesPerTS)
		t.lastTS = ts
	}
	return t.absFrame
}

// Advance moves the frame cursor by n frames and returns the new absolute
// frame number. It is used by subsystems that consume decoded PCM directly
// rather than RTP timestamps.
func (t *Timestamp) Advance(n uint64) uint64 {
	t.absFrame += n
	t.absTimestamp += n * uint64(t.framesPerTS)
	if t.hasTS {
		t.lastTS = uint32(t.absTimestamp)
	}
	return t.absFrame
}

// FrameRange describes a half-open interval of absolute sample frames.
type FrameRange struct {
	Start uint64
	End   uint64
}

// Len is the number of frames in the range.
func (r FrameRange) Len() uint64 { return r.End - r.Start }
