package model

// RNG is a small deterministic pseudo-random generator (SplitMix64). It is
// intentionally not math/rand: the sequence is fixed by this file, so fixtures
// and digests remain identical across Go releases and platforms.
type RNG struct {
	state uint64
}

// NewRNG seeds a generator. A zero seed is remapped so it still produces a
// useful sequence.
func NewRNG(seed int64) *RNG {
	s := uint64(seed)
	if s == 0 {
		s = 0x9e3779b97f4a7c15
	}
	return &RNG{state: s}
}

// Next returns the next 64-bit value.
func (r *RNG) Next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// Intn returns a value in [0,n). n must be positive.
func (r *RNG) Intn(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return int64(r.Next() % uint64(n))
}

// Chance returns true with probability numerator/denominator.
func (r *RNG) Chance(numerator, denominator int64) bool {
	if denominator <= 0 {
		return false
	}
	return r.Intn(denominator) < numerator
}

// Float returns a value in [0,1).
func (r *RNG) Float() float64 {
	return float64(r.Next()>>11) / float64(1<<53)
}
