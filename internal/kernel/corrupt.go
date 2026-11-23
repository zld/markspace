package kernel

import (
	"bytes"
	"os"
	"path/filepath"

	"mediad/internal/ebml"
	"mediad/internal/fixture"
	"mediad/internal/pipeline"
	"mediad/internal/timeline"
)

// runCorruptions evaluates all five corruption fixtures against the stage that
// must reject each one. Partial outputs would mean a corrupted input leaked
// into committed results.
func runCorruptions(base *fixture.Result, seed int64) CorruptReport {
	cases, err := fixture.Corrupt(base, seed)
	var rep CorruptReport
	if err != nil {
		return rep
	}
	rep.Cases = len(cases)
	for _, c := range cases {
		rejected, partial, detail := evaluateCorruption(c, base)
		if rejected {
			rep.Rejected++
		}
		if partial {
			rep.PartialOutputs++
		}
		rep.Details = append(rep.Details, detail)
	}
	return rep
}

func evaluateCorruption(c fixture.CorruptCase, base *fixture.Result) (rejected, partial bool, detail string) {
	switch c.Kind {
	case fixture.CorruptCacheTamper:
		rejected, partial = cacheTamperRejected()
		return rejected, partial, "cache_digest_tamper"
	case fixture.CorruptKeyframeMisalign:
		return misalignRejected(c, base), false, "keyframe_misalign"
	case fixture.CorruptTimestampReverse:
		return timestampReverseRejected(c, base), false, "timestamp_reverse"
	}
	if c.Data == nil {
		return false, false, string(c.Kind)
	}
	if _, err := ebml.Parse(bytes.NewReader(c.Data), ebml.DefaultParseOptions(), nil); err == nil {
		return false, false, string(c.Kind) + ": parsed without error"
	}
	return true, false, string(c.Kind)
}

func misalignRejected(c fixture.CorruptCase, base *fixture.Result) bool {
	if len(c.MutatedKeyframes) == 0 {
		return false
	}
	_, err := timeline.Align(timeline.AlignInput{
		Representations: []timeline.Representation{
			{ID: "base", Bandwidth: 500, Frames: base.VideoKeyframes, Duration: base.DurationNS},
			{ID: "mutated", Bandwidth: 900, Frames: c.MutatedKeyframes, Duration: base.DurationNS},
		},
		TargetDuration: 1_000_000_000,
		Tolerance:      33_333_333,
	})
	return err != nil
}

// timestampReverseRejected parses the corrupted bytes and feeds them through
// the timeline clock, which must reject the reversed timestamp. The parser
// itself has no reason to reject it, so rejection is a timeline-stage proof.
func timestampReverseRejected(c fixture.CorruptCase, base *fixture.Result) bool {
	if c.Data == nil {
		return false
	}
	doc, err := ebml.Parse(bytes.NewReader(c.Data), ebml.DefaultParseOptions(), func(b ebml.Block) error {
		return nil
	})
	if err != nil {
		return false
	}
	clocks := map[uint64]*timeline.TrackClock{}
	var blocks int
	_, err = ebml.Parse(bytes.NewReader(c.Data), ebml.DefaultParseOptions(), func(b ebml.Block) error {
		blocks++
		tc := clocks[b.Track]
		if tc == nil {
			tc, err = timeline.NewTrackClock(doc.TimecodeScale, 33_333_333, 0)
			if err != nil {
				return err
			}
			clocks[b.Track] = tc
		}
		_, observeErr := tc.Observe(b.Timecode, b.Source.Offset)
		return observeErr
	})
	_ = blocks
	return err != nil
}

func cacheTamperRejected() (bool, bool) {
	dir, err := os.MkdirTemp("", "mediad-cache-*")
	if err != nil {
		return false, false
	}
	defer os.RemoveAll(dir)
	cache := pipeline.Cache{Dir: dir}
	_ = cache.Put("input", "config", []byte("good"))
	content := filepath.Join(dir, cache.Key("input", "config")+".manifest")
	_ = os.WriteFile(content, []byte("tampered"), 0o644)
	hit, err := cache.Get("input", "config")
	if err != nil {
		return false, false
	}
	return hit.Quarantined && !hit.Found, false
}
