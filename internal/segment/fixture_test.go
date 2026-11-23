package segment

import (
	"crypto/sha256"

	"mediad/internal/ebml"
	"mediad/internal/index"
)

// testSource provides deterministic fake bytes addressed by span so digest
// behavior can be tested without real files.
type testSource struct {
	buf []byte
}

func newTestSource(n int) *testSource {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + 3)
	}
	return &testSource{buf: b}
}

func (s *testSource) hashSpan(sp ebml.Span) ([32]byte, error) {
	if sp.Offset < 0 || sp.End() > int64(len(s.buf)) {
		return [32]byte{}, ErrSpanMismatch
	}
	return sha256.Sum256(s.buf[sp.Offset:sp.End()]), nil
}

func fakeIndex() *index.Index {
	tracks := []ebml.Track{
		{Number: 1, Type: ebml.TrackVideo},
		{Number: 2, Type: ebml.TrackAudio},
	}
	ix := index.New(tracks)
	// Video keyframe every 2s, 30 fps duration 6s.
	for i := 0; i < 6; i++ {
		ix.Add(ebml.TrackVideo, ebml.Block{
			Track: 1, Keyframe: true, Timecode: int64(i * 2000),
			Source: ebml.Span{Offset: int64(100 + i*100), Length: 100},
		}, int64(i)*2_000_000_000)
	}
	// Audio every 500ms.
	for i := 0; i < 25; i++ {
		ix.Add(ebml.TrackAudio, ebml.Block{
			Track: 2, Timecode: int64(i * 500),
			Source: ebml.Span{Offset: int64(2000 + i*20), Length: 20},
		}, int64(i)*500_000_000)
	}
	ix.Finalize()
	return ix
}
