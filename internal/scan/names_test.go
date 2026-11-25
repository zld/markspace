package scan

import (
	"os"
	"sort"
	"testing"
)

// fragmentNamesForTest lists fragment files in numeric (input-index) order.
func fragmentNamesForTest(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if len(e.Name()) >= 5 && e.Name()[:5] == "frag-" {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return names[i] < names[j]
	})
	return names
}
