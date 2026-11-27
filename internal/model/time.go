package model

import (
	"math/bits"
)

// MasterTimescale is the single time basis every demuxed timeline is
// converted into. 90 kHz divides the common media timescales (24k, 25/30 fps
// 90k-style RTP, 48k) without loss at the rational level.
const MasterTimescale uint32 = 90_000

// Rational is an exact signed fraction. All time math goes through it or
// integer ticks; floating point is never used to accumulate time.
type Rational struct {
	Num int64
	Den int64 // always positive
}

// NewRational validates and normalizes a fraction (sign on numerator, gcd 1).
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, NewError(CodeMalformedContainer, "zero denominator rational")
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rational{Num: 0, Den: 1}, nil
	}
	g := gcd64(abs64(num), uint64(den))
	return Rational{Num: num / int64(g), Den: den / int64(g)}, nil
}

// MustRational panics on invalid input; for compile-time constants in tests.
func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

func (r Rational) Add(o Rational) Rational {
	num := r.Num*o.Den + o.Num*r.Den
	den := r.Den * o.Den
	out, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return out
}

func (r Rational) Sub(o Rational) Rational {
	return r.Add(Rational{Num: -o.Num, Den: o.Den})
}

// MulInt scales by an integer without introducing a denominator.
func (r Rational) MulInt(v int64) Rational {
	out, err := NewRational(r.Num*v, r.Den)
	if err != nil {
		panic(err)
	}
	return out
}

// Cmp returns -1/0/1.
func (r Rational) Cmp(o Rational) int {
	diff := r.Num*o.Den - o.Num*r.Den
	switch {
	case diff < 0:
		return -1
	case diff > 0:
		return 1
	default:
		return 0
	}
}

// Floor truncates toward negative infinity to an integer tick.
func (r Rational) Floor() int64 {
	q := r.Num / r.Den
	if r.Num < 0 && r.Num%r.Den != 0 {
		q--
	}
	return q
}

// Round returns the nearest integer tick, halves away from zero.
func (r Rational) Round() int64 {
	if r.Num >= 0 {
		return (r.Num + r.Den/2) / r.Den
	}
	return -((-r.Num + r.Den/2) / r.Den)
}

// ScaleToMaster converts srcTick measured in srcTimescale into exact master
// time and the rounded integer master tick.
func ScaleToMaster(srcTick int64, srcTimescale uint32) (Rational, int64) {
	if srcTimescale == 0 {
		return Rational{}, 0
	}
	exact, err := NewRational(srcTick*int64(MasterTimescale), int64(srcTimescale))
	if err != nil {
		panic(err)
	}
	return exact, exact.Round()
}

// DeltaMaster converts a source-timescale duration into master ticks.
func DeltaMaster(delta uint64, srcTimescale uint32) int64 {
	if srcTimescale == 0 {
		return 0
	}
	r, err := NewRational(int64(delta)*int64(MasterTimescale), int64(srcTimescale))
	if err != nil {
		panic(err)
	}
	return r.Round()
}

// ExpandClock maps a raw, possibly-wrapped timestamp onto an unwrapped
// extended axis using modular arithmetic. prevRaw is the previous sample's
// raw value and prevExtended its unwrapped value. A numerically smaller raw
// value means the 32-bit field crossed zero, which adds exactly one modulus
// cycle; a forward step within a cycle leaves the cycle untouched. The raw
// input is never mutated; wrapped reports that a boundary crossing occurred.
func ExpandClock(prevRaw, prevExtended, raw, modulus uint64) (extended uint64, wrapped bool) {
	if modulus == 0 {
		return raw, false
	}
	if prevExtended == 0 && prevRaw == 0 {
		// First observation anchors the axis without a spurious wrap.
		return raw, false
	}
	base := prevExtended - prevRaw // multiple of modulus: start of this cycle
	if raw < prevRaw {
		return base + modulus + raw, true
	}
	return base + raw, false
}

func gcd64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func abs64(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}

// BitLen32 exposes the modulus choice for clock expansion tests.
func BitLen32(v uint32) int { return bits.Len32(v) }

// MetaSource identifies which container supplied a metadata value.
type MetaSource string

const (
	SourceMXF     MetaSource = "mxf"
	SourceMP4     MetaSource = "mp4"
	SourceDefault MetaSource = "default"
)

// Field records a metadata verdict: the adopted value, where it came from and
// the raw original text. Conflicting fields are never silently overwritten.
type Field[T any] struct {
	Value  T          `json:"value"`
	Source MetaSource `json:"source"`
	Raw    string     `json:"raw"`
}

// Rotation in degrees; only multiples of 90 are valid.
type Rotation struct {
	Degrees int32 `json:"degrees"`
}

// Crop is expressed in source pixels from each edge.
type Crop struct {
	Left   uint32 `json:"left"`
	Top    uint32 `json:"top"`
	Right  uint32 `json:"right"`
	Bottom uint32 `json:"bottom"`
}

// Color is the nclx-style color description carried by MXF/MP4 containers.
type Color struct {
	Primaries uint16 `json:"primaries"`
	Transfer  uint16 `json:"transfer"`
	Matrix    uint16 `json:"matrix"`
	FullRange bool   `json:"fullRange"`
}

// TrackMeta is the container-neutral per-track metadata view.
type TrackMeta struct {
	TrackID   uint32          `json:"trackId"`
	Timescale uint32          `json:"timescale"`
	Handler   string          `json:"handler"`
	Codec     string          `json:"codec"`
	Rotation  Field[Rotation] `json:"rotation"`
	Crop      Field[Crop]     `json:"crop"`
	Color     Field[Color]    `json:"color"`
}

// Sample is one compressed sample with its full source provenance. Payload is
// never re-encoded; remux copies these bytes verbatim.
type Sample struct {
	Source      string `json:"source"`
	SourceTrack uint32 `json:"sourceTrack"`
	TargetTrack uint32 `json:"targetTrack"`
	Index       uint32 `json:"index"`
	DTS         int64  `json:"dtsTick"`      // master timescale, pre-repair order
	Duration    int64  `json:"durationTick"` // master timescale
	SrcOffset   int64  `json:"srcOffset"`    // payload offset in source file
	Size        uint32 `json:"size"`
	RTPRaw      uint32 `json:"rtpRaw,omitempty"`
	RTPCycles   uint32 `json:"rtpCycles,omitempty"`
	Payload     []byte `json:"-"`
}

// Boundary records one timeline edge with all three time views required for
// audit: the original timestamp, the repaired monotonic timestamp and the
// offset between them.
type Boundary struct {
	EventSeq     uint64 `json:"eventSeq"`
	Source       string `json:"source"`
	TrackID      uint32 `json:"trackId"`
	SampleIndex  uint32 `json:"sampleIndex"`
	OriginalTick int64  `json:"originalTick"`
	RepairedTick int64  `json:"repairedTick"`
	Delta        int64  `json:"deltaTick"`
	Repair       string `json:"repair,omitempty"`
}
