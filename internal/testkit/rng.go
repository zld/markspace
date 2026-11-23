package testkit

import (
	"math/rand"
)

// RNG is a deterministic generator wrapper. Every fixture is a pure function
// of the seed, so the demo and verifier can reproduce byte-identical input.
type RNG struct{ r *rand.Rand }

// NewRNG returns a generator for seed.
func NewRNG(seed int64) *RNG {
	return &RNG{r: rand.New(rand.NewSource(seed))}
}

// Intn returns a pseudo-random int in [0,n).
func (g *RNG) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return g.r.Intn(n)
}

// Bytes returns n deterministic bytes.
func (g *RNG) Bytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.r.Intn(256))
	}
	return b
}
