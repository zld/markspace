package mp4

import (
	"bytes"
	"fmt"

	abema "github.com/abema/go-mp4"
)

// AbemaValidate runs the third-party go-mp4 structural reader over one
// fragment and returns the top-level box types it observed. This is an
// independent cross-check of the hand-written incremental scanner: both
// parsers must agree on the box structure of every fragment we remux.
func AbemaValidate(data []byte) ([]string, error) {
	r := bytes.NewReader(data)
	var types []string
	_, err := abema.ReadBoxStructure(r, func(h *abema.ReadHandle) (interface{}, error) {
		// Path includes the current box, so depth 1 means top level.
		if len(h.Path) == 1 {
			types = append(types, h.BoxInfo.Type.String())
		}
		return h.Expand()
	})
	if err != nil {
		return nil, fmt.Errorf("go-mp4 validation: %w", err)
	}
	return types, nil
}

// AbemaCountBoxes walks the whole fragment and returns the total box
// count including nested boxes, used by tests to confirm parser agreement.
func AbemaCountBoxes(data []byte) (int, error) {
	r := bytes.NewReader(data)
	count := 0
	_, err := abema.ReadBoxStructure(r, func(h *abema.ReadHandle) (interface{}, error) {
		count++
		return h.Expand()
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
