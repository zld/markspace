package media

// Unified media timebase shared by every module. All frame and audio track
// timestamps are validated against this single clock so analysis results
// share one time reference.
const (
	// Timescale is the unified media clock in ticks per second (90 kHz).
	Timescale = 90000
	// SeqModulus is the sequence number modulus (16-bit wire field).
	SeqModulus = 1 << 16
	// TSModulus is the raw timestamp modulus (32-bit wire field).
	TSModulus = 1 << 32
)

// SeqDelta returns the signed distance from sequence a to b modulo 2^16,
// in the range (-32768, 32767].
func SeqDelta(a, b uint16) int32 {
	return int32(int16(b - a))
}

// UnwrapTimestamp expands a raw 32-bit timestamp into a monotonic 64-bit
// tick count by choosing the representative closest to ref.
func UnwrapTimestamp(raw uint32, ref int64) int64 {
	base := ref &^ (TSModulus - 1)
	best := base + int64(raw)
	for _, c := range [2]int64{best - TSModulus, best + TSModulus} {
		if abs64(c-ref) < abs64(best-ref) {
			best = c
		}
	}
	return best
}

// ExtendSeq maps a raw 16-bit sequence number into the extended 64-bit
// sequence space anchored near ref.
func ExtendSeq(raw uint16, ref int64) int64 {
	base := ref &^ (SeqModulus - 1)
	best := base + int64(raw)
	for _, c := range [2]int64{best - SeqModulus, best + SeqModulus} {
		if abs64(c-ref) < abs64(best-ref) {
			best = c
		}
	}
	return best
}

// TicksToMs converts unified timebase ticks to milliseconds.
func TicksToMs(t int64) float64 { return float64(t) * 1000.0 / Timescale }

// MsToTicks converts milliseconds to unified timebase ticks.
func MsToTicks(ms float64) int64 { return int64(ms * Timescale / 1000.0) }

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
