package clock

import (
	"github.com/pion/rtp"

	"medpipe/internal/apperr"
)

// RTPPoint is one packet clock in source packet order.  RawTimestamp is never
// overwritten; Timestamp is the sign-extended/expanded monotonic tick count.
type RTPPoint struct {
	Sample       int    `json:"sample"`
	Sequence     uint16 `json:"sequence"`
	RawTimestamp uint32 `json:"raw_timestamp"`
	Timestamp    int64  `json:"monotonic_timestamp"`
	RelativeTick int64  `json:"relative_tick"`
	ByteOffset   int64  `json:"byte_offset,omitempty"`
}

// WrapEvent records one observed 32-bit wrap.
type WrapEvent struct {
	FromSample int    `json:"from_sample"`
	ToSample   int    `json:"to_sample"`
	BeforeRaw  uint32 `json:"before_raw"`
	AfterRaw   uint32 `json:"after_raw"`
}

// ParseRTP parses one MP4 sample payload as an RTP packet without discarding
// the original 32-bit timestamp.
func ParseRTP(sample int, payload []byte, byteOffset int64) (RTPPoint, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(payload); err != nil {
		return RTPPoint{}, apperr.New("E_RTP_PARSE", "parse RTP packet for sample %d: %v", sample, err).
			WithStage("repair").WithSample(sample, byteOffset).WithRecoverable(false)
	}
	return RTPPoint{
		Sample:       sample,
		Sequence:     pkt.SequenceNumber,
		RawTimestamp: pkt.Timestamp,
		Timestamp:    int64(pkt.Timestamp),
		ByteOffset:   byteOffset,
	}, nil
}

// UnwrapRTP expands raw 32-bit timestamps in packet order.  Packet order is
// authoritative; sequence numbers are retained but not reordered.
func UnwrapRTP(points []RTPPoint) ([]RTPPoint, []WrapEvent) {
	out := make([]RTPPoint, len(points))
	copy(out, points)
	wraps := make([]WrapEvent, 0)
	if len(out) == 0 {
		return out, wraps
	}
	out[0].Timestamp = int64(out[0].RawTimestamp)
	for i := 1; i < len(out); i++ {
		prevRaw := out[i-1].RawTimestamp
		curRaw := out[i].RawTimestamp
		// Cast subtraction through uint32 then int32 gives the modular,
		// sign-extended shortest delta around the 2^32 timestamp ring.
		delta := int64(int32(curRaw - prevRaw))
		out[i].Timestamp = out[i-1].Timestamp + delta
		if curRaw < prevRaw && delta > 0 {
			wraps = append(wraps, WrapEvent{
				FromSample: out[i-1].Sample,
				ToSample:   out[i].Sample,
				BeforeRaw:  prevRaw,
				AfterRaw:   curRaw,
			})
		}
	}
	origin := out[0].Timestamp
	for i := range out {
		out[i].RelativeTick = out[i].Timestamp - origin
	}
	return out, wraps
}

// Boundary contains the two sides of one source switch.
type Boundary struct {
	SwitchID            string
	Track               string
	SourceFile          string
	Sample              int
	Timescale           uint32
	PrevTick            int64
	CurrentTick         int64
	PreviousDuration    int64
	LastTick            int64
	LastDuration        int64
	FirstAffectedSample int
	LastAffectedSample  int
	ByteOffset          int64
	LeftAnchorShift     *int64
	RightAnchorShift    *int64
}

// Repair is an auditable offset correction.
type Repair struct {
	Kind                string   `json:"kind"`
	SwitchID            string   `json:"switch_id,omitempty"`
	Track               string   `json:"track"`
	SourceFile          string   `json:"source_file,omitempty"`
	Source              string   `json:"source,omitempty"`
	Sample              int      `json:"sample"`
	ByteOffset          int64    `json:"byte_offset,omitempty"`
	AffectedSamples     [2]int   `json:"affected_samples"`
	IntervalStartTick   int64    `json:"interval_start_tick"`
	IntervalEndTick     int64    `json:"interval_end_tick"`
	OriginalTimeTick    int64    `json:"original_time_tick"`
	CorrectedTimeTick   int64    `json:"corrected_time_tick"`
	OriginalOffsetTicks int64    `json:"original_offset_ticks"`
	RepairOffsetTicks   int64    `json:"repair_offset_ticks"`
	ResidualTicks       int64    `json:"residual_ticks"`
	Basis               string   `json:"basis"`
	OriginalTime        Rational `json:"original_time"`
	CorrectedTime       Rational `json:"corrected_time"`
	OffsetTime          Rational `json:"offset_time"`
	RepairTime          Rational `json:"repair_time"`
}

// Isolation records a track that cannot be safely aligned.
type Isolation struct {
	Track             string `json:"track"`
	SourceFile        string `json:"source_file,omitempty"`
	FirstSample       int    `json:"first_sample"`
	LastSample        int    `json:"last_sample"`
	IntervalStartTick int64  `json:"interval_start_tick"`
	IntervalEndTick   int64  `json:"interval_end_tick"`
	JumpTicks         int64  `json:"jump_ticks"`
	Reason            string `json:"reason"`
	ByteOffset        int64  `json:"byte_offset,omitempty"`
}

// Decision is either an auditable repair or an isolation interval.
type Decision struct {
	Repair    *Repair    `json:"repair,omitempty"`
	Isolation *Isolation `json:"isolation,omitempty"`
}

// ResolveBoundary applies the <=1 second continuity rule and the >1 second
// dual-anchor rule.  It never guesses when anchors are absent or disagree.
func ResolveBoundary(b Boundary) Decision {
	expected := b.PrevTick + b.PreviousDuration
	originalOffset := b.CurrentTick - expected
	intervalEnd := b.LastTick + b.LastDuration
	base := Repair{
		SwitchID:            b.SwitchID,
		Track:               b.Track,
		SourceFile:          b.SourceFile,
		Sample:              b.Sample,
		ByteOffset:          b.ByteOffset,
		AffectedSamples:     [2]int{b.FirstAffectedSample, b.LastAffectedSample},
		IntervalStartTick:   b.CurrentTick,
		IntervalEndTick:     intervalEnd,
		OriginalTimeTick:    b.CurrentTick,
		OriginalOffsetTicks: originalOffset,
		OriginalTime:        TickTime(b.CurrentTick, b.Timescale),
		OffsetTime:          TickTime(originalOffset, b.Timescale),
	}

	offsetSeconds := TickTime(originalOffset, b.Timescale).Abs()
	oneSecond := NewRational(1, 1)
	if offsetSeconds.Cmp(oneSecond) <= 0 {
		shift := -originalOffset
		base.Kind = "boundary-continuity"
		base.RepairOffsetTicks = shift
		base.RepairTime = TickTime(shift, b.Timescale)
		base.CorrectedTimeTick = b.CurrentTick + shift
		base.CorrectedTime = TickTime(base.CorrectedTimeTick, b.Timescale)
		base.ResidualTicks = base.CorrectedTimeTick - expected
		base.Basis = "jump magnitude <= 1 second; extended previous sample duration as continuous boundary"
		r := base
		return Decision{Repair: &r}
	}

	iso := Isolation{
		Track:             b.Track,
		SourceFile:        b.SourceFile,
		FirstSample:       b.FirstAffectedSample,
		LastSample:        b.LastAffectedSample,
		IntervalStartTick: b.CurrentTick,
		IntervalEndTick:   intervalEnd,
		JumpTicks:         originalOffset,
		ByteOffset:        b.ByteOffset,
	}
	if b.LeftAnchorShift != nil && b.RightAnchorShift != nil {
		var diff int64
		if d := *b.LeftAnchorShift - *b.RightAnchorShift; d >= 0 {
			diff = d
		} else {
			diff = -d
		}
		if diff <= 1 {
			shift := *b.LeftAnchorShift
			base.Kind = "dual-anchor"
			base.RepairOffsetTicks = shift
			base.RepairTime = TickTime(shift, b.Timescale)
			base.CorrectedTimeTick = b.CurrentTick + shift
			base.CorrectedTime = TickTime(base.CorrectedTimeTick, b.Timescale)
			base.ResidualTicks = diff
			base.Basis = "jump > 1 second; both source anchors present and agree within one track tick"
			r := base
			return Decision{Repair: &r}
		}
		iso.Reason = "E_ANCHOR_DISAGREEMENT: anchors differ by more than one tick"
	} else {
		iso.Reason = "E_ANCHOR_MISSING: jump exceeds one second without two consistent anchors"
	}
	return Decision{Isolation: &iso}
}
