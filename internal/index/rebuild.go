// Package index rebuilds a keyframe (sync sample) index from the samples
// that survived parsing, loss and recovery, and reports which GOP
// segments are recoverable. The index is materialized as an MP4 stss box
// on the MP4 timescale so downstream packagers can consume it.
package index

import (
	"bytes"
	"crypto/sha256"

	mp4 "github.com/abema/go-mp4"

	"mediad/internal/model"
)

// Segment describes one GOP-sized run of samples.
type Segment struct {
	StartSample int
	EndSample   int // exclusive
	Recoverable bool
}

// Result is the rebuilt index plus segment classification.
type Result struct {
	Keyframes     []uint32 // 1-based sample numbers, stss convention
	Segments      []Segment
	Recoverable   int
	Unrecoverable int
	StssSHA256    [32]byte // digest of the marshaled stss box
	StssSize      int
	Stss          []byte
}

// Rebuild walks the final video sample stream (emission order, gaps
// already resolved as far as recovery allowed) and produces the sync
// sample index. A GOP starting at a sequence discontinuity is
// unrecoverable, as is a GOP with no decodable keyframe.
func Rebuild(samples []*model.Event) Result {
	var res Result
	type gop struct {
		start int
		key   bool
		holed bool
	}
	var gops []gop
	var prevSeq uint64
	for i, ev := range samples {
		discontinuous := i > 0 && ev.Seq != prevSeq+1
		if ev.Keyframe || len(gops) == 0 {
			gops = append(gops, gop{start: i, key: ev.Keyframe, holed: discontinuous})
		} else if discontinuous {
			gops[len(gops)-1].holed = true
		}
		prevSeq = ev.Seq
	}
	for gi, g := range gops {
		end := len(samples)
		if gi+1 < len(gops) {
			end = gops[gi+1].start
		}
		ok := g.key && !g.holed
		res.Segments = append(res.Segments, Segment{
			StartSample: g.start, EndSample: end, Recoverable: ok,
		})
		if ok {
			res.Recoverable++
			res.Keyframes = append(res.Keyframes, uint32(g.start+1))
		} else {
			res.Unrecoverable++
		}
	}
	// Materialize the index as an stss box on the MP4 timescale.
	stss := mp4.Stss{
		EntryCount:   uint32(len(res.Keyframes)),
		SampleNumber: res.Keyframes,
	}
	var buf bytes.Buffer
	if n, err := mp4.Marshal(&buf, &stss, mp4.Context{}); err == nil {
		res.StssSize = int(n)
		res.Stss = buf.Bytes()
		res.StssSHA256 = sha256.Sum256(res.Stss)
	}
	return res
}
