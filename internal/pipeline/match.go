package pipeline

import (
	"strings"

	"mediad/internal/clock"
	"mediad/internal/index"
	"mediad/internal/stream"
)

// fillMatch re-parses the index against the consumed access units and
// derives the keyframe hit rate, round-trip rate and corrupt segment count.
func fillMatch(res *Result, aus []*stream.AccessUnit, opts Options, tb *clock.TimeBase) {
	raws, err := index.Parse(strings.NewReader(string(opts.IndexText)))
	if err != nil {
		return
	}
	idx, err := index.Validate(opts.Codec, raws, int64(len(opts.Stream)))
	if err != nil {
		return
	}
	res.Match = index.MatchTimeline(aus, idx, tb)
	res.KeyframeHitRate = res.Match.HitRate
}
