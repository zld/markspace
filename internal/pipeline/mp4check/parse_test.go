package mp4check

import (
	"os"
	"testing"

	"github.com/abema/go-mp4"
)

// TestFixtureSegmentsParse walks every produced box tree with go-mp4 to ensure
// the writer emits a structurally complete file, not merely one our own reader
// accepts. It runs only when MEDIARUN_SEGMENTS points at a run directory, so it
// never depends on a previous manual invocation in normal `go test ./...`.
func TestFixtureSegmentsParse(t *testing.T) {
	dir := os.Getenv("MEDIARUN_SEGMENTS")
	if dir == "" {
		t.Skip("MEDIARUN_SEGMENTS not set")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := dir + "/" + e.Name()
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
			types = append(types, h.BoxInfo.Type.String())
			if h.BoxInfo.Type == mp4.BoxTypeMdat() {
				return nil, nil
			}
			if h.BoxInfo.IsSupportedType() {
				if _, _, err := h.ReadPayload(); err != nil {
					return nil, err
				}
			}
			return h.Expand()
		})
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		t.Logf("%s: %v", e.Name(), types)
	}
}
