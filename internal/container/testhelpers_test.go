package container

import (
	"os"
	"testing"
)

// readFile is a test helper; damaged fixtures are small by design.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// findFirstMagic returns the offset of the first sample record magic.
func findFirstMagic(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == 0xAF && b[i+1] == 0x01 {
			return i
		}
	}
	return -1
}
