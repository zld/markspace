package main

// rng is a tiny deterministic LCG (Numerical Recipes constants). The demo must
// be reproducible, so it never uses the global math/rand source or the clock
// for randomness: a fixed seed always yields the same fixtures.
type rng struct {
	seed  int64
	state uint64
}

func newRNG(seed int64) *rng {
	return &rng{seed: seed, state: uint64(seed)}
}

func (r *rng) next() uint64 {
	const (
		mul = 6364136223846793005
		inc = 1442695040888963407
	)
	r.state = r.state*mul + inc
	return r.state
}

// intn returns a deterministic value in [0,n).
func (r *rng) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// int16n fills a deterministic PCM waveform; values stay non-silent and s16.
func (r *rng) pcm(n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		v := int16((int(r.next()%4001) - 2000))
		if v == 0 {
			v = int16(7 + r.intn(20))
		}
		out[i] = v
	}
	return out
}
