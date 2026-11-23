package pipeline

import (
	"context"
	"testing"

	"archivecheck/internal/codec"
	"archivecheck/internal/sample"
)

// TestGeneratedLayersProduceExpectedCounts cross-checks the pipeline's reported
// unit, keyframe and parse counts against a direct parse of the same fixture.
func TestGeneratedLayersProduceExpectedCounts(t *testing.T) {
	c := cfg("ok")
	out, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := sample.Generate(c.Case, c.Seed, c.ChunkBytes, c.Targets)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for i, l := range archive.Layers {
		byName[l.Name] = i
	}
	for _, row := range out.Manifest.Layers {
		l := archive.Layers[byName[row.Layer]]
		if row.Codec != string(l.Codec) {
			t.Fatalf("layer %s codec=%s want %s", row.Layer, row.Codec, l.Codec)
		}
		if row.Keyframes != len(l.Index.Entries) {
			t.Fatalf("layer %s keyframes=%d entries=%d", row.Layer, row.Keyframes, len(l.Index.Entries))
		}
		if row.RecoveredSegments != row.Keyframes {
			t.Fatalf("layer %s segments=%d keyframes=%d", row.Layer, row.RecoveredSegments, row.Keyframes)
		}
		switch l.Codec {
		case codec.H264, codec.H265:
			p, _ := codec.NewNALParser(l.Codec)
			for off := 0; off < len(l.Stream); off += lEncChunk {
				end := off + lEncChunk
				if end > len(l.Stream) {
					end = len(l.Stream)
				}
				p.Feed(l.Stream[off:end])
			}
			p.Flush()
			units, keys := p.Counts()
			if row.Units != units || row.Keyframes != keys {
				t.Fatalf("layer %s units=%d/%d keys=%d/%d", row.Layer, row.Units, units, row.Keyframes, keys)
			}
		default:
			p := codec.NewAV1Parser()
			for off := 0; off < len(l.Stream); off += lEncChunk {
				end := off + lEncChunk
				if end > len(l.Stream) {
					end = len(l.Stream)
				}
				p.Feed(l.Stream[off:end])
			}
			p.Flush()
			units, _, keys := p.Counts()
			if row.Units != units || row.Keyframes != keys {
				t.Fatalf("layer %s units=%d/%d keys=%d/%d", row.Layer, row.Units, units, row.Keyframes, keys)
			}
		}
	}
}

const lEncChunk = 1024
