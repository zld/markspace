package pipeline

import "sort"

// sortFloat64s orders latency samples in place. It is isolated so the main
// orchestration file stays focused on the stage wiring.
func sortFloat64s(v []float64) {
	sort.Float64s(v)
}
