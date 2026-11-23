// Package limits centralizes the bounded-size guardrails that prevent
// unbounded memory allocation when parsing untrusted containers.
package limits

import "mediapipe/internal/fault"

// Max* constants bound every dimension of declared input. Any violation is a
// non-recoverable limit error for the offending unit.
const (
	MaxFileBytes    int64 = 512 << 20 // 512 MiB
	MaxBoxBytes     int64 = 128 << 20 // 128 MiB per single MP4 box
	MaxKLVElements        = 200_000
	MaxBoxDepth           = 16
	MaxTracks             = 64
	MaxSamplesPerTrack    = 200_000
	MaxPackets            = 500_000
	MaxDurationNanos      = int64(6 * 3600 * 1_000_000_000) // 6 hours
	MaxPayloadBytes       = 16 << 20                         // 16 MiB per unit
	MaxColorProfiles      = 8
)

// Exceeded builds a non-recoverable limit error.
func Exceeded(file, what string, offset int64, have, max any) *fault.Error {
	return fault.New(
		kindLimit(),
		"limit exceeded for "+what+" (have "+itoa(have)+", max "+itoa(max)+")",
		file, offset, false,
	)
}

func kindLimit() fault.Kind { return fault.KindLimit }

// itoa avoids importing strconv to keep the helper dependency free.
func itoa(v any) string {
	switch x := v.(type) {
	case int:
		return formatInt(int64(x))
	case int64:
		return formatInt(x)
	case uint64:
		return formatUint(x)
	case uint32:
		return formatUint(uint64(x))
	default:
		return "?"
	}
}

func formatInt(v int64) string {
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

func formatUint(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
