package stream

// KeyframeInterval is the gap between two adjacent key-frame samples,
// expressed both in unified media ticks and in sample count.
type KeyframeInterval struct {
	FromIndex      int
	ToIndex        int
	Ticks          int64
	SamplesBetween int
}

// KeyframeReport summarizes random-access placement in a scan.
type KeyframeReport struct {
	KeyIndices []int
	Intervals  []KeyframeInterval
	// MaxTicks / MaxSamples describe the widest observed interval; both
	// are zero when fewer than two key frames exist.
	MaxTicks   int64
	MaxSamples int
}

// AnalyzeKeyframes walks scan output in sample order. Only IDR-class
// NAL units mark a key frame, and adjacent key frames define one
// interval; parameter sets alone never count as key frames.
func AnalyzeKeyframes(res ScanResult) KeyframeReport {
	rep := KeyframeReport{}
	for i := range res.Samples {
		if res.Samples[i].KeyFrame {
			rep.KeyIndices = append(rep.KeyIndices, i)
		}
	}
	for k := 1; k < len(rep.KeyIndices); k++ {
		from, to := rep.KeyIndices[k-1], rep.KeyIndices[k]
		iv := KeyframeInterval{
			FromIndex:      from,
			ToIndex:        to,
			Ticks:          res.Samples[to].Time.Start - res.Samples[from].Time.Start,
			SamplesBetween: to - from,
		}
		rep.Intervals = append(rep.Intervals, iv)
		if iv.Ticks > rep.MaxTicks {
			rep.MaxTicks = iv.Ticks
		}
		if iv.SamplesBetween > rep.MaxSamples {
			rep.MaxSamples = iv.SamplesBetween
		}
	}
	return rep
}
