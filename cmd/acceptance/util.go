package main

import (
	"os"

	"mediacheck/internal/cache"
	"mediacheck/internal/gap"
)

func mkdirAll(path string) error { return os.MkdirAll(path, 0o755) }

func newGapLedger(trackID string, rate uint32) *gap.Ledger {
	return gap.NewLedger(trackID, rate)
}

func mustConfigDigest(v any) string {
	d, err := cache.ConfigDigest(v)
	if err != nil {
		panic(err)
	}
	return d
}
