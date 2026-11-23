package kernel

import (
	"mediad/internal/ebml"
	"mediad/internal/index"
)

func rejectionIndex() *index.Index {
	ix := index.New([]ebml.Track{{Number: 1, Type: ebml.TrackVideo}})
	for i := 0; i < 4; i++ {
		ix.Add(ebml.TrackVideo, ebml.Block{
			Track: 1, Keyframe: true,
			Source: ebml.Span{Offset: int64(i * 5), Length: 5},
		}, int64(i)*1_000_000_000)
	}
	ix.Finalize()
	return ix
}

func spanHasherForTest(src []byte) func(ebml.Span) ([32]byte, error) {
	return spanHasher(src)
}
