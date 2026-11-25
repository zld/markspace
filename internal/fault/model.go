// Package fault defines the shared error model used by every pipeline
// stage. Faults are localized to packet sequence numbers, byte ranges and
// media time so that damaged input never aborts the whole analysis.
package fault

// Class is the machine-readable category of a localized fault.
type Class string

const (
	// ClassTruncatedHead: an RTP packet whose header is shorter than the
	// minimum 12 bytes or whose declared payload does not fit the datagram.
	ClassTruncatedHead Class = "truncated_header"
	// ClassIllegalLength: a declared length field (RTP payload, FU
	// fragment or box size) that is inconsistent with the bytes present.
	ClassIllegalLength Class = "illegal_length"
	// ClassMissingPacket: one or more sequence numbers never observed
	// before the reorder window advanced past them.
	ClassMissingPacket Class = "missing_packet"
	// ClassUnknownNAL: a NAL unit whose NAL type is outside the
	// H.264/H.265 ranges this kernel knows how to scan.
	ClassUnknownNAL Class = "unknown_nal"
	// ClassTruncatedNAL: a NAL unit that ends without its final RTP
	// marker because the stream ended or a gap cut it short.
	ClassTruncatedNAL Class = "truncated_nal"
	// ClassOverWindow: a gap whose first missing packet fell outside the
	// bounded reorder window and therefore cannot be bounded precisely.
	ClassOverWindow Class = "over_window_gap"
	// ClassMalformedRTCP: an RTCP compound packet that cannot be parsed.
	ClassMalformedRTCP Class = "malformed_rtcp"
)

// SeqRange is an inclusive, fully expanded (post-wrap) RTP sequence range.
type SeqRange struct {
	First uint64
	Last  uint64
}

// Len returns the number of sequence numbers in the range.
func (s SeqRange) Len() uint64 {
	if s.Last < s.First {
		return 0
	}
	return s.Last - s.First + 1
}

// ByteRange is a half-open span inside the reassembled media byte stream.
type ByteRange struct {
	Offset int64
	Length int64
}

// End returns the exclusive end offset.
func (b ByteRange) End() int64 {
	return b.Offset + b.Length
}

// TimeRange is an inclusive span on the unified media clock (RTP ticks).
type TimeRange struct {
	Start int64
	End   int64
}

// Fault describes one localized problem. Zero-value Seq/Byte/Time fields
// mean that dimension could not be established, never that zero is real.
type Fault struct {
	Class      Class
	Codec      string
	Seq        SeqRange
	Byte       ByteRange
	Time       TimeRange
	NALType    uint32
	OverWindow bool
	Detail     string
}

// Recorder collects faults while a stream is processed. It is not safe
// for concurrent use; a pipeline owns one recorder per track.
type Recorder struct {
	faults []Fault
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Add appends a fault.
func (r *Recorder) Add(f Fault) { r.faults = append(r.faults, f) }

// All returns the recorded faults in insertion order.
func (r *Recorder) All() []Fault {
	out := make([]Fault, len(r.faults))
	copy(out, r.faults)
	return out
}

// CountByClass returns how many faults of each class were recorded.
func (r *Recorder) CountByClass() map[Class]int {
	m := make(map[Class]int)
	for _, f := range r.faults {
		m[f.Class]++
	}
	return m
}

// Total returns the number of recorded faults.
func (r *Recorder) Total() int { return len(r.faults) }
