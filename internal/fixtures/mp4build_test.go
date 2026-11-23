package fixtures

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

func TestBuildAudioBytesProbes(t *testing.T) {
	pcm := make([]byte, 4*1024*2) // 1024 stereo frames
	spec := audioFileSpec{
		movieTimescale:  48000,
		mediaTimescale:  48000,
		trackID:         1,
		sampleRate:      48000,
		channels:        2,
		bitDepth:        16,
		sampleFrames:    256,
		samplesPerChunk: 2,
		pcm:             pcm,
		opts:            defaultOpts(),
	}
	raw, err := buildAudioBytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	info, err := mp4.Probe(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("tracks %d", len(info.Tracks))
	}
	tr := info.Tracks[0]
	if tr.Timescale != 48000 {
		t.Fatalf("timescale %d", tr.Timescale)
	}
	if len(tr.Samples) != 8 {
		t.Fatalf("samples %d", len(tr.Samples))
	}
	if len(tr.Chunks) != 4 {
		t.Fatalf("chunks %d", len(tr.Chunks))
	}
	if info.Timescale != 48000 {
		t.Fatalf("movie timescale %d", info.Timescale)
	}
}

func TestBuildAudioBytesFastStart(t *testing.T) {
	pcm := make([]byte, 8*1024*2)
	spec := audioFileSpec{
		movieTimescale: 48000, mediaTimescale: 48000, trackID: 1,
		sampleRate: 48000, channels: 2, bitDepth: 16,
		sampleFrames: 1024, samplesPerChunk: 1,
		pcm: pcm, opts: writeOpts{movieBeforeMedia: true, corruptChunkIndex: -1, corruptSampleIndex: -1},
	}
	raw, err := buildAudioBytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	info, err := mp4.Probe(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !info.FastStart {
		t.Fatal("expected fast-start layout")
	}
	if info.Tracks[0].Chunks[0].DataOffset == 0 {
		t.Fatal("chunk offset not resolved")
	}
}

func TestByteBufferSeekPatch(t *testing.T) {
	var b byteBuffer
	b.Write([]byte("abcd"))
	b.Seek(1, 0)
	b.Write([]byte("XY"))
	if string(b.Bytes()) != "aXYd" {
		t.Fatalf("got %q", b.Bytes())
	}
}
