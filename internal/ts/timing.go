package ts

// Clock ticks are expressed in 90kHz units, the MPEG-TS reference rate.
const (
	TicksPerSecond = 90000
	// Max33 is the modulo of PTS/DTS and PCR-base fields.
	Max33 int64 = 1 << 33
)

// Clock tracks a 33-bit 90kHz timestamp stream and converts raw, wrapping
// values into a monotonic tick count that never decreases.
type Clock struct {
	last  int64
	rolls int64
	has   bool
}

// Push feeds one raw timestamp and returns the unwrapped, monotonic value.
// A backward jump of more than half the 33-bit range is treated as wraparound.
func (c *Clock) Push(raw int64) int64 {
	if !c.has {
		c.has = true
		c.last = raw
		return raw
	}
	delta := raw - (c.last - c.rolls*Max33)
	if delta < -Max33/2 {
		c.rolls++
	} else if delta > Max33/2 && c.rolls > 0 {
		c.rolls-- // tolerate a re-feed after wraparound noise
	}
	c.last = raw
	return c.rolls*Max33 + raw
}

// Rolls reports how many 33-bit wraparounds have been observed.
func (c *Clock) Rolls() int64 { return c.rolls }

// Seconds converts 90kHz ticks to seconds.
func Seconds(ticks int64) float64 { return float64(ticks) / TicksPerSecond }

// Timeline accumulates PTS observations for one PID and surfaces gaps.
type Timeline struct {
	pts     Clock
	lastPTS int64
	has     bool
	// Wraps counts PTS wraparound events seen on this timeline.
	Wraps int64
}

// ObservePTS feeds an unwrapped PTS and flags a backward movement that was
// not explainable as a 33-bit wraparound (true discontinuity).
func (t *Timeline) ObservePTS(raw int64) (unwrapped int64, backward bool) {
	prevRolls := t.pts.rolls
	unwrapped = t.pts.Push(raw)
	if t.pts.rolls > prevRolls {
		t.Wraps++
	}
	if t.has && unwrapped < t.lastPTS {
		backward = true
	}
	t.lastPTS = unwrapped
	t.has = true
	return unwrapped, backward
}

// PCR tracks a 27MHz program clock reference (base*300 + extension).
type PCR struct {
	base Clock // 33-bit, 90kHz-equivalent base
}

// Push feeds raw PCR base/extension values and returns unwrapped base ticks.
func (p *PCR) Push(base int64, _ uint16) int64 { return p.base.Push(base) }

// NAL types relevant to slicing (ITU-T H.264 Table 7-1).
const (
	NALNonIDR = 1
	NALIDR    = 5
	NALSPS    = 7
	NALPPS    = 8
)

// NALUnit is one Annex B network abstraction layer unit.
type NALUnit struct {
	Type    byte // forbidden_zero_bit/nal_ref_idc stripped
	Payload []byte
}

// SplitAnnexB splits an H.264 byte stream payload into NAL units, tolerating
// both 3-byte (00 00 01) and 4-byte (00 00 00 01) start codes.
func SplitAnnexB(b []byte) []NALUnit {
	var starts []int
	for i := 0; i+2 < len(b); {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			starts = append(starts, i+3)
			i += 3
			continue
		}
		if i+3 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
			starts = append(starts, i+4)
			i += 4
			continue
		}
		i++
	}
	out := make([]NALUnit, 0, len(starts))
	for n, start := range starts {
		end := len(b)
		if n+1 < len(starts) {
			// Back over the start code (3 or 4 bytes).
			next := starts[n+1]
			end = next - 3
			if next-4 >= 0 && b[next-4] == 0 {
				end--
			}
		}
		for end > start && b[end-1] == 0 {
			end--
		}
		if end <= start {
			continue
		}
		out = append(out, NALUnit{Type: b[start] & 0x1F, Payload: b[start:end]})
	}
	return out
}

// ContainsIDR reports whether the payload starts with or contains an IDR NAL.
func ContainsIDR(b []byte) bool {
	for _, n := range SplitAnnexB(b) {
		if n.Type == NALIDR {
			return true
		}
	}
	return false
}

// FirstNALType returns the type of the first NAL unit, or -1 when absent.
func FirstNALType(b []byte) int {
	units := SplitAnnexB(b)
	if len(units) == 0 {
		return -1
	}
	return int(units[0].Type)
}
