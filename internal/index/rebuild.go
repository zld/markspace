// Package index rebuilds a keyframe (sync sample) index from the samples
// that survived parsing, loss and recovery, and reports which GOP
// segments are recoverable. The index is materialized as an MP4 stss
// box on the MP4 timescale so downstream packagers can consume it.
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
	Keyframes     []uint32  // 1-based sample numbers, stss convention
	Segments      []Segment
	Recoverable   int
	Unrecoverable int
	StssSHA256    [32]byte // digest of the marshaled stss box
	StssSize      int
}

// Rebuild walks the final video sample stream (emission order, gaps
// already resolved as far as recovery allowed) and produces the sync
// sample index. lost reports whether an extended sequence number left a
// permanent hole; a GOP containing any hole is unrecoverable, as is a
// trailing GOP with no decodable keyframe.
func Rebuild(samples []*model.Event, lost func(seq uint64) bool) Result {
	var res Result
	// sampleHasHole[i] is true when the GOP containing sample i saw an
	// unrecovered loss; tracked per GOP below.
	type gop struct {
		start  int
		key    bool
		holed  bool
	}
	var gops []gop
	for i, ev := range samples {
		if ev.Keyframe || len(gops) == 0 {
			gops = append(gops, gop{start: i, key: ev.Keyframe})
		}
		if lost(ev.Seq) {
			gops[len(gops)-1].holed = true
		}
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
		res.StssSHA256 = sha256.Sum256(buf.Bytes())
	}
	return res
}
