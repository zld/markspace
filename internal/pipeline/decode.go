// Package pipeline wires ordering, codec parsing, audio processing, muxing,
// caching and checkpointing into one reusable local processing kernel.
package pipeline

import (
	"math"

	"github.com/example/mediad/internal/audio"
	"github.com/example/mediad/internal/codec"
	"github.com/example/mediad/internal/model"
)

// Params is the full processing specification for one run. It is the
// non-material half of the cache key.
type Params struct {
	TargetRate uint32
	Layout     audio.Layout
	LUFS       float64
	TruePeak   float64
	MaxBytes   int64
	BudgetMs   int64
}

// synthPCM turns parsed packet durations into deterministic float64 PCM. The
// kernel does not include a real Opus/AAC decoder; instead each accepted
// packet contributes a reproducible low-amplitude tone for exactly its parsed
// sample span, so timing alignment and every downstream stage operate on real
// per-packet sample counts rather than estimates.
func synthPCM(frames []parsedPacket, sourceRate uint32, ch int) audio.Buffer {
	total := uint64(0)
	for _, f := range frames {
		total += uint64(f.samples)
	}
	buf := audio.NewBuffer(int(total), ch)
	pos := 0
	for _, f := range frames {
		for i := uint32(0); i < f.samples; i++ {
			// Deterministic tone well inside the true-peak ceiling.
			v := 0.2 * math.Sin(2*math.Pi*float64(pos+int(i))/8.0)
			for c := 0; c < ch; c++ {
				buf.Samples[(pos+int(i))*ch+c] = v
			}
		}
		pos += int(f.samples)
	}
	return buf
}

type parsedPacket struct {
	seq     uint16
	offset  int64
	samples uint32
}

// parseAll applies the codec parser to every accepted packet and stops on the
// first malformed packet, returning a stable stage/seq/offset error.
func parseAll(parser codec.Parser, pkts []model.Packet) ([]parsedPacket, error) {
	out := make([]parsedPacket, 0, len(pkts))
	for _, p := range pkts {
		frame, err := parser.Parse(p.Sequence, p.Offset, p.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, parsedPacket{
			seq: p.Sequence, offset: p.Offset, samples: frame.Samples,
		})
	}
	return out, nil
}
