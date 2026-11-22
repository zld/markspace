package main

// Report carries the acceptance metrics of one pipeline run.
type Report struct {
	OutputSHA256 [32]byte
}

// Print is implemented in a later batch.
func (r *Report) Print() {}

// ThresholdsOK is implemented in a later batch.
func (r *Report) ThresholdsOK() bool { return false }

// Invariants is implemented in a later batch.
func (r *Report) Invariants(deterministic bool) (int, int) { return 0, 1 }
