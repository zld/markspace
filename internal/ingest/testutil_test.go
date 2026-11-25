package ingest

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeTruncatedBox writes an ftyp header that claims 64 bytes but stops after
// the 8-byte header, exercising the truncation path without fixtures.
func writeTruncatedBox(t *testing.T, dir string) string {
	t.Helper()
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf, 64)
	copy(buf[4:], "ftyp")
	path := filepath.Join(dir, "truncated.mp4")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
