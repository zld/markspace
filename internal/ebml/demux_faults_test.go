package ebml_test

import (
	"errors"
	"io"
	"testing"

	"mediad/internal/ebml"
	"mediad/internal/testkit"
)

func TestDemuxSparseAudio(t *testing.T) {
	spec := testkit.DefaultDocSpec()
	spec.ClipDurMs = 8_000
	spec.Tracks[1].SparseEvery = 3
	doc, err := testkit.BuildDoc(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, blocks, err := runDemux(t, doc.Data)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("demux: %v", err)
	}
	var video, audio int
	for _, b := range blocks {
		switch b.TrackNumber {
		case 1:
			video++
		case 2:
			audio++
		}
	}
	if video == 0 || audio == 0 {
		t.Fatalf("video=%d audio=%d", video, audio)
	}
	// Every third audio block is dropped, so audio should be well under the
	// 2x rate a complete 50 Hz track would deliver against 25 fps video.
	if full := video * 2; audio > full*3/4 {
		t.Fatalf("audio (%d) not sparse relative to video (%d, full=%d)", audio, video, full)
	}
}

func TestDemuxWindowedReads(t *testing.T) {
	// A tiny reader window exercises the bounded-streaming path.
	doc, err := testkit.BuildDoc(testkit.DefaultDocSpec())
	if err != nil {
		t.Fatal(err)
	}
	var blocks int
	w := ebml.NewWalker(&chunkReader{data: doc.Data, chunk: 7}, ebml.ReaderOptions{})
	d := ebml.NewDemuxer(w)
	d.OnBlock = func(ebml.Block) { blocks++ }
	if _, err := d.Run(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v", err)
	}
	if blocks != len(doc.Blocks) {
		t.Fatalf("blocks = %d want %d", blocks, len(doc.Blocks))
	}
}

type chunkReader struct {
	data  []byte
	chunk int
	i     int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.data) {
		return 0, io.EOF
	}
	n := r.chunk
	if n > len(p) {
		n = len(p)
	}
	if r.i+n > len(r.data) {
		n = len(r.data) - r.i
	}
	copy(p, r.data[r.i:r.i+n])
	r.i += n
	return n, nil
}
