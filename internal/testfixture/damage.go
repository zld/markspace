// Package testfixture: transport damage applied to a clean RTP capture.
// The damage is fully seed-driven so demo runs are reproducible.
package testfixture

import (
	"math/rand"
)

// DamageSpec describes how a clean packet sequence is degraded in transit.
type DamageSpec struct {
	Seed          int64
	LossBurst     int // size of one contiguous burst to remove
	LossAt        int // index where the burst starts
	ReorderWindow int // neighborhood size for swapping adjacent packets
	DuplicateAt   int // index of one packet duplicated in-window
	LateAt        int // index of a packet pushed far outside the window
}

// ApplyDamage returns a new packet slice reflecting the spec. The clean input
// is left untouched so the caller can compare before/after statistics.
func ApplyDamage(packets [][]byte, spec DamageSpec) [][]byte {
	rng := rand.New(rand.NewSource(spec.Seed + 99))
	n := len(packets)
	keep := make([]bool, n)
	for i := range keep {
		keep[i] = true
	}
	// Burst loss.
	for i := spec.LossAt; i < n && i < spec.LossAt+spec.LossBurst; i++ {
		keep[i] = false
	}
	var out [][]byte
	for i, p := range packets {
		if keep[i] {
			out = append(out, p)
		}
	}
	// Reordering: swap a few neighbors within the reorder neighborhood.
	if spec.ReorderWindow > 1 && len(out) > 2 {
		swaps := 2 + rng.Intn(3)
		for k := 0; k < swaps; k++ {
			i := rng.Intn(len(out) - 1)
			out[i], out[i+1] = out[i+1], out[i]
		}
	}
	// In-window duplicate: insert a copy right next to its original so the
	// reorder window (not the late discard) classifies it as a duplicate.
	if spec.DuplicateAt >= 0 && spec.DuplicateAt < len(out) {
		dup := append([]byte(nil), out[spec.DuplicateAt]...)
		pos := spec.DuplicateAt + 1
		out = append(out[:pos], append([][]byte{dup}, out[pos:]...)...)
	}
	// Out-of-window late arrival: keep the in-order backbone intact and append
	// one already-delivered early packet at the very tail. By then the base has
	// advanced far beyond the window span, so this copy is discarded and counted
	// as late without opening a false gap or disturbing later packets.
	if spec.LateAt >= 0 && spec.LateAt < len(packets) && keep[spec.LateAt] {
		out = append(out, append([]byte(nil), packets[spec.LateAt]...))
	}
	return out
}
