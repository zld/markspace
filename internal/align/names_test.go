package align

import (
	"os"
	"sort"
	"testing"
)

func fragmentNames(t *testing.T, dir string) []string {
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
	sort.Strings(names)
	return names
}
