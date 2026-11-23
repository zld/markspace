package repair

import (
	"io"
	"testing"

	"mediad/internal/ebml"
)

// demux runs the real demuxer over a generated document.
func demux(t *testing.T, data []byte) ([]ebml.Block, []ebml.TrackInfo, error) {
	t.Helper()
	d := ebml.NewDemuxer(ebml.NewWalker(&sliceReader{b: data}, ebml.ReaderOptions{}))
	var blocks []ebml.Block
	d.OnBlock = func(b ebml.Block) { blocks = append(blocks, b) }
	_, err := d.Run()
	return blocks, d.Info().Tracks, err
}

type sliceReader struct {
	b []byte
	i int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
