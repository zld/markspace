package kernel

import (
	"os"

	"mediad/internal/ebml"
	"mediad/internal/segment"
)

func segmentLoad(path string) (*segment.Manifest, error) { return segment.LoadManifest(path) }

func writeManifest(path string, m *segment.Manifest) error {
	b, err := segment.MarshalCanonical(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func segmentForTest(data []byte) segment.Segment {
	return segment.Segment{Keyframe: ebml.Span{Offset: 0, Length: int64(len(data))}}
}
