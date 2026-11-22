// Package fixture generates fully self-contained, deterministic healthy
// and damaged media samples: multi-bitrate DASH representations, an HLS
// playlist with discontinuities, fragmented ISO BMFF segments (parsed with
// go-mp4) and synthetic RTP packet streams. No external media is used.
package fixture

// rng is a deterministic xorshift64* PRNG so every seed reproduces the
// exact same manifests, segments and packet streams.
type rng struct {
	state uint64
}

func newRNG(seed int64) *rng {
	s := uint64(seed)
	if s == 0 {
		s = 0x9E3779B97F4A7C15
	}
	return &rng{state: s}
}

func (r *rng) next() uint64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 0x2545F4914F6CDD1D
}

// intn returns a deterministic value in [0, n); n must be positive.
func (r *rng) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// intRange returns a deterministic value in [lo, hi].
func (r *rng) intRange(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + r.intn(hi-lo+1)
}

// booln returns true with probability numerator/denominator.
func (r *rng) booln(numerator, denominator int) bool {
	if denominator <= 0 {
		return false
	}
	return r.intn(denominator) < numerator
}
