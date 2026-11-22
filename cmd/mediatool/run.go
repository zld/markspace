package main

import "fmt"

type runConfig struct {
	Seed    int64
	Tracks  int
	Workers int
	Faults  string
}

// RunOnce is implemented in a later batch.
func RunOnce(cfg runConfig) (*Report, error) {
	return nil, fmt.Errorf("RunOnce not implemented yet")
}
