// Package batch runs the measurement pipeline over file lists with
// bounded concurrency. A failing file never drops other results.
package batch

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"loudcheck/internal/audio"
	"loudcheck/internal/measure"
	"loudcheck/internal/report"
)

// Options configures a batch run.
type Options struct {
	Concurrency int              // max files processed in parallel
	Gates       report.Gates     // pass/fail thresholds
	Weights     []float64        // per-channel override, nil for layout defaults
}

// AnalyzeFile measures one WAV file; any failure lands in the report's
// Error field rather than aborting the batch.
func AnalyzeFile(path string, o Options) (rep report.FileReport) {
	rep.Path = path
	defer func() {
		if r := recover(); r != nil {
			rep = report.FileReport{Path: path, Error: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	rd, err := audio.Open(path)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	defer rd.Close()
	info := rd.Info()
	rep.Channels = info.Channels
	rep.SampleRate = info.SampleRate
	rep.DurationSec = float64(info.Frames) / float64(info.SampleRate)

	weights := o.Weights
	if weights == nil {
		weights, err = measure.ChannelWeights(info.Channels)
	} else if len(weights) != info.Channels {
		err = fmt.Errorf("weight override has %d entries, file has %d channels",
			len(weights), info.Channels)
	}
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	lm, err := measure.NewLoudnessMeter(info.SampleRate, weights)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	tm := measure.NewTruePeakMeter(info.Channels)
	for {
		block, err := rd.ReadBlock(8192)
		if err != nil {
			rep.Error = err.Error()
			return rep
		}
		if block == nil {
			break
		}
		for c := range block {
			for _, v := range block[c] {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					rep.Error = "non-finite sample value"
					return rep
				}
			}
		}
		lm.AddBlock(block)
		tm.AddBlock(block)
	}
	lr := lm.Result()
	rep.IntegratedLUFS = lr.Integrated
	rep.ShortTermMax = lr.ShortTermMax
	rep.TruePeakDBTP = tm.DBTP()
	rep.BlocksTotal = lr.BlocksTotal
	rep.BlocksAbs = lr.BlocksAbs
	rep.BlocksRel = lr.BlocksRel
	rep.LoudnessStatus = lr.Status
	rep.Evaluate(o.Gates)
	return rep
}

// Run measures every path with bounded concurrency and returns reports
// sorted by path for stable output, plus an aggregate summary.
func Run(paths []string, o Options) ([]report.FileReport, report.Summary) {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	reps := make([]report.FileReport, len(paths))
	sem := make(chan struct{}, o.Concurrency)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reps[i] = AnalyzeFile(p, o)
		}()
	}
	wg.Wait()
	sort.SliceStable(reps, func(i, j int) bool { return reps[i].Path < reps[j].Path })
	sum := report.Summary{Total: len(reps)}
	for _, r := range reps {
		switch {
		case r.Error != "":
			sum.Failed++
		case r.OverallPass != nil && !*r.OverallPass:
			sum.Succeeded++
			sum.GateFailed++
		default:
			sum.Succeeded++
		}
	}
	return reps, sum
}
