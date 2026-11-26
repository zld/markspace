package clock

// Mapper converts an unwrapped source clock with a potentially non-zero origin
// to origin-relative ticks on one integer time base.
type Mapper struct {
	Base       TimeBase `json:"time_base"`
	OriginTick int64    `json:"origin_tick"`
}

// NewMapper creates a source-to-relative time mapper.
func NewMapper(base TimeBase, originTick int64) Mapper {
	return Mapper{Base: base, OriginTick: originTick}
}

// Relative removes the non-zero source origin without modifying raw values.
func (m Mapper) Relative(monotonicTick int64) int64 {
	return monotonicTick - m.OriginTick
}

// Time maps an origin-relative tick count to exact rational seconds.
func (m Mapper) Time(relativeTick int64) Rational {
	return m.Base.Time(relativeTick)
}

// ConvertTicks maps an integer tick count from one time base to another.  The
// boolean is false when the result is not an integral destination tick.
func ConvertTicks(ticks int64, from, to TimeBase) (int64, bool) {
	return to.Ticks(from.Time(ticks))
}
