package index

import (
	"fmt"

	"mp4seek/internal/bmff"
)

// Skeleton: table loaders are filled in by follow-up commits.

func loadBox(r *bmff.Reader, byType map[string]bmff.Box, typ, path string) (*cursor, error) {
	return nil, fmt.Errorf("loadBox not implemented")
}

func loadSizes(byType map[string]bmff.Box, r *bmff.Reader, path string) ([]uint32, error) {
	return nil, fmt.Errorf("loadSizes not implemented")
}

func sampleOffsets(byType map[string]bmff.Box, r *bmff.Reader, path string, sizes []uint32) ([]int64, error) {
	return nil, fmt.Errorf("sampleOffsets not implemented")
}

func loadCtts(byType map[string]bmff.Box, r *bmff.Reader, path string, n int) ([]int64, error) {
	return nil, fmt.Errorf("loadCtts not implemented")
}

func loadStss(byType map[string]bmff.Box, r *bmff.Reader, path string, n int) ([]bool, error) {
	return nil, fmt.Errorf("loadStss not implemented")
}
