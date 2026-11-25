package probe

import (
	"os"

	"github.com/abema/go-mp4"
)

// libraryInspect uses github.com/abema/go-mp4 as an independent box-tree reader.
// Our bounded scanner owns precise recovery semantics; the library count is retained
// as provenance and to ensure generated files exercise the pinned dependency.
func libraryInspect(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	count := 0
	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (any, error) {
		count++
		switch h.BoxInfo.Type.String() {
		case "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "udta":
			return h.Expand()
		default:
			return nil, nil
		}
	})
	return count, err
}
