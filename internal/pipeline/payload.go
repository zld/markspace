package pipeline

import (
	"os"

	"liveregress/internal/fault"
	"liveregress/internal/fmp4"
)

// readPayload returns the concatenated sample bytes of a remuxed segment.
func readPayload(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, path, -1, "read remuxed segment", err)
	}
	seg, err := fmp4.ParseSegmentBytes(data, path)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, f := range seg.Fragments {
		for _, s := range f.Samples {
			end := s.Offset + int64(s.Size)
			if end > int64(len(data)) {
				return nil, fault.New(fault.KindTruncate, path, s.Offset,
					"remuxed sample past end of file").Recover(false)
			}
			out = append(out, data[s.Offset:end]...)
		}
	}
	return out, nil
}
