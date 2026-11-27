package mux_test

import (
	"errors"
	"os"

	"github.com/zld/continuity/internal/mux"
)

func writeAll(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func audioSpecs(n int) []mux.SampleSpec {
	out := make([]mux.SampleSpec, n)
	for i := range out {
		// 48 kHz audio, 1024-sample AAC-style frames.
		out[i] = mux.SampleSpec{Delta: 1024, Sync: false}
	}
	return out
}

func errorIs(err error, target error) bool {
	return errors.Is(err, target)
}
