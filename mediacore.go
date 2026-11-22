// Package mediacore defines the integer-nanosecond event model, track
// identifiers, bounded parser limits and classified error structure that are
// shared by every subsystem of the media pipeline.
//
// All time accumulation in the pipeline uses integer ticks (nanoseconds).
// Floating point is only used inside bounded drift estimation and never feeds
// segment hashes, so repeated runs of the same inputs produce identical
// outputs.
package mediacore

// Bounded parser limits. Parsers reject inputs that would require allocations
// beyond these limits instead of trusting container headers.
const (
	// MaxBlockBytes bounds a single PCM block or video sample payload.
	MaxBlockBytes = 4 << 20 // 4 MiB
	// MaxChannels bounds the channel count of an audio block / WAV file.
	MaxChannels = 64
	// MaxTracks bounds the number of tracks accepted from a container.
	MaxTracks = 32
	// MaxSegments bounds the number of segments a timeline may commit.
	MaxSegments = 1 << 20
)

// Nanosecond scale helpers.
const (
	Nanosecond  int64 = 1
	Microsecond       = 1_000 * Nanosecond
	Millisecond       = 1_000 * Microsecond
	Second            = 1_000 * Millisecond
)

// TrackID identifies a track across every subsystem.
type TrackID string

// Standard track identifiers used by the generated fixtures and the demo.
const (
	TrackAudio1 TrackID = "audio/1"
	TrackVideo1 TrackID = "video/1"
)

// EventKind classifies an Event.
type EventKind uint8

const (
	KindUnknown EventKind = iota
	KindRTP               // RTP packet as received (pre-buffer)
	KindPCM               // reordered PCM block emitted by the jitter buffer
	KindVideo             // video frame sample
	KindGap               // deterministic missing-block gap record
	KindSenderReport      // clock anchor (RTP timestamp paired with master time)
	KindLate              // packet that arrived after the buffer window
)

// Event is the unit exchanged between subsystems. Times are integer
// nanoseconds. Time is the mapped master-clock time; OrigTime retains the
// original device time before any drift correction.
type Event struct {
	Track    TrackID
	Kind     EventKind
	Time     int64
	OrigTime int64
	Recv     int64 // unpack/receive time on the master clock
	Seq      uint64
	RTPTS    uint32 // raw RTP timestamp (wrapping)
	Samples  int    // audio sample frames carried (0 for non-PCM)
	Channels int
	Rate     int // audio sample rate in Hz (0 when not applicable)
	Keyframe bool
	Payload  []byte
	Reason   string // machine-readable annotation, e.g. gap cause
}

// Duration returns the media duration of an audio event in nanoseconds.
func (e Event) Duration() int64 {
	if e.Rate <= 0 || e.Samples <= 0 {
		return 0
	}
	return int64(e.Samples) * Second / int64(e.Rate)
}

// Category is the failure category recorded for corrupt or boundary inputs.
type Category string

const (
	CatCorrupt Category = "corrupt" // truncated container / invalid layout
	CatGap     Category = "gap"     // unrecovered missing sample block
	CatOverlap Category = "overlap" // blocks overlap on the same track
	CatLayout  Category = "layout"  // channel change / length not divisible by channels
	CatLate    Category = "late"    // arrival after the buffer window
	CatDrift   Category = "drift"   // drift beyond configured ppm bound
	CatLimit   Category = "limit"   // parser limit / out-of-bounds allocation
	CatEmpty   Category = "empty"   // empty track or container
)

// Error is the classified error structure shared by all subsystems. It
// records the failure category together with the track, processing stage and
// byte/sample offset at which the failure was detected.
type Error struct {
	Cat    Category
	Track  TrackID
	Stage  string
	Offset int64
	Msg    string
	Err    error
}

func (e *Error) Error() string {
	s := string(e.Cat)
	if e.Stage != "" {
		s += " at " + e.Stage
	}
	if e.Track != "" {
		s += " track=" + string(e.Track)
	}
	if e.Offset != 0 {
		s += " offset=" + itoa(e.Offset)
	}
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap supports errors.Is/errors.As through wrapped parser errors.
func (e *Error) Unwrap() error { return e.Err }

// AsError extracts a *mediacore.Error from err.
func AsError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	if me, ok := err.(*Error); ok {
		return me, true
	}
	type wrapper interface{ Unwrap() error }
	for {
		w, ok := err.(wrapper)
		if !ok {
			return nil, false
		}
		err = w.Unwrap()
		if me, ok := err.(*Error); ok {
			return me, true
		}
		if err == nil {
			return nil, false
		}
	}
}

// NewError builds a classified error.
func NewError(cat Category, track TrackID, stage string, offset int64, msg string) *Error {
	return &Error{Cat: cat, Track: track, Stage: stage, Offset: offset, Msg: msg}
}

// WrapError wraps an underlying parser error with classification.
func WrapError(cat Category, track TrackID, stage string, offset int64, msg string, err error) *Error {
	return &Error{Cat: cat, Track: track, Stage: stage, Offset: offset, Msg: msg, Err: err}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
