package pipeline

import (
	"sort"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/fanin"
	"example.com/mediatimeline/internal/fanout"
	"example.com/mediatimeline/internal/index"
	"example.com/mediatimeline/internal/model"
)

// stageFanout expands the video track into the ladder.
func stageFanout(ctx *stageContext) error {
	layers := fanout.DefaultLadder(ctx.in.Ladder, 1920, 1080)
	corrupt := map[int]bool{}
	corruptLayer := -1
	damaged := ctx.in.CorruptLayer || ctx.delivery.LostPackets > 0
	if damaged {
		// A damaged delivery fragment makes the finest rung unavailable; the
		// coarser rungs are recovered from the archive copy and stay
		// indexable. One layer failing must not contaminate the others. With
		// a single-rung ladder that rung is the only one, so it fails alone.
		corruptLayer = layers[len(layers)-1].Index
		for i := range ctx.videoSamples {
			corrupt[i] = true
		}
	}
	res, err := fanout.Build(layers, fanout.LayerInput{
		Track: model.TrackVideoMain, Timescale: ctx.videoTimescale,
		Samples: ctx.videoSamples, CorruptIndices: corrupt, CorruptLayer: corruptLayer,
	}, ctx.in.SegmentMS)
	if err != nil {
		return err
	}
	ctx.fanoutResult = res
	if err := addErrors(ctx.rep, res.Errors); err != nil {
		return err
	}
	return nil
}

// stageFanin validates layer boundaries against the shared clock.
func stageFanin(ctx *stageContext) error {
	segTicks, err := segmentTicks(ctx.in.SegmentMS, ctx.videoTimescale)
	if err != nil {
		return err
	}
	merge, err := fanin.Merge(ctx.fanoutResult, fanin.Options{
		ToleranceTicks: 1000, Timescale: ctx.videoTimescale,
		SegmentTicks: segTicks, RequireAllLayers: ctx.in.RequireAllLayers,
	})
	ctx.merge = merge
	ctx.rep.Segments = len(merge.Segments)
	if err := addErrors(ctx.rep, merge.Errors); err != nil {
		return err
	}
	return nil
}

func segmentTicks(segmentMS int64, timescale uint32) (int64, error) {
	d, err := clock.New(segmentMS, 1000)
	if err != nil {
		return 0, err
	}
	return d.FloorTicks(timescale)
}

// stageIndex builds the keyframe index and segment hashes for the lowest
// available layer, then verifies the hashes by recomputing them.
func stageIndex(ctx *stageContext) error {
	var chosen *fanout.Layer
	for i := range ctx.merge.Layers {
		l := &ctx.merge.Layers[i]
		if l.Available {
			chosen = l
			break
		}
	}
	if chosen == nil {
		return model.NewError(model.TrackVideoMain, model.StageIndex, model.CodeLayerUnavailable, false,
			"no available layer to index")
	}
	segs := make([]index.Segment, len(ctx.merge.Segments))
	ticks := make([]int64, len(ctx.merge.Segments))
	for i, s := range ctx.merge.Segments {
		segs[i] = index.Segment{Index: s.Index, StartTick: s.StartTick, EndTick: s.EndTick}
		ticks[i] = s.StartTick
	}
	offsets := make([]uint64, len(chosen.Samples))
	for i := range offsets {
		offsets[i] = uint64(i) * 1024
	}
	kf, err := index.BuildKeyframes(chosen.Track, chosen.Timescale, chosen.Samples, offsets, ticks)
	if err != nil {
		return err
	}
	ctx.rep.KeyframeIndex = kf
	ctx.rep.Keyframes = len(kf.Entries)
	data := func(i int) ([]byte, bool) {
		src := chosen.SampleIndices[i]
		p, ok := ctx.videoBytes[src]
		return p, ok
	}
	segIndexes, err := index.BuildSegments(chosen.Track, chosen.Timescale, chosen.Samples, data, segs, nil)
	if err != nil {
		return err
	}
	ctx.rep.SegmentIndexes = segIndexes
	matched, total, err := index.VerifySegments(chosen.Track, chosen.Timescale, chosen.Samples, data, segs, segIndexes, nil)
	if err != nil {
		return err
	}
	if total == 0 {
		ctx.rep.SegmentHashMatch = 1
	} else {
		ctx.rep.SegmentHashMatch = float64(matched) / float64(total)
	}
	ctx.rep.KeyframeHitRate = keyframeHitRate(kf, chosen.Samples)
	return nil
}

// keyframeHitRate is the fraction of reference keyframes (sync samples of the
// full track) that the index can reach exactly.
func keyframeHitRate(kf index.KeyframeIndex, all []model.Sample) float64 {
	var want []int64
	for _, s := range all {
		if !s.Sync {
			continue
		}
		r, err := clock.FromModel(s.Time.RepairedTime)
		if err != nil {
			continue
		}
		t, err := r.FloorTicks(kf.Timescale)
		if err != nil {
			continue
		}
		want = append(want, t)
	}
	if len(want) == 0 {
		return 1
	}
	hits := 0
	for _, t := range want {
		e, ok := kf.Lookup(t)
		if ok && e.Ticks == t {
			hits++
		}
	}
	return float64(hits) / float64(len(want))
}

// sortStrings is a small helper kept for deterministic report rendering.
func sortStrings(ss []string) []string {
	s := append([]string(nil), ss...)
	sort.Strings(s)
	return s
}
