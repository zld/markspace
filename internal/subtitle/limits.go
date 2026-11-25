package subtitle

// Format identifies a subtitle serialization.
type Format string

const (
	FormatSRT Format = "srt"
	FormatVTT Format = "vtt"
)

const (
	DefaultMaxInputBytes = 1 << 20
	DefaultMaxLineBytes  = 4096
	DefaultMaxCues       = 10_000
)

// Limits bounds parser memory and work.
type Limits struct {
	MaxInputBytes int
	MaxLineBytes  int
	MaxCues       int
}

// DefaultLimits returns the conservative parser limits.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes: DefaultMaxInputBytes,
		MaxLineBytes:  DefaultMaxLineBytes,
		MaxCues:       DefaultMaxCues,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxInputBytes <= 0 {
		l.MaxInputBytes = d.MaxInputBytes
	}
	if l.MaxLineBytes <= 0 {
		l.MaxLineBytes = d.MaxLineBytes
	}
	if l.MaxCues <= 0 {
		l.MaxCues = d.MaxCues
	}
	return l
}
