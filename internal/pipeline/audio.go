package pipeline

import (
	"fmt"
	"math"
	"os"

	"regional.example/mediad/internal/audio"
	"regional.example/mediad/internal/clock"
	"regional.example/mediad/internal/fault"
	"regional.example/mediad/internal/manifest"
	mediam "regional.example/mediad/internal/mp4"
)

// Validate checks options against supported ranges before any file is opened.
func (o *Options) Validate() error {
	if o.OutputRate != 48000 {
		return fault.Fatal(fault.ClassAudio, "output-rate", fmt.Sprintf("output rate %d is not 48000", o.OutputRate))
	}
	if _, err := o.OutputLayout.Channels(); err != nil {
		return fault.Fatal(fault.ClassAudio, "layout", err.Error())
	}
	if o.TargetLUFS < audio.MinTargetLUFS || o.TargetLUFS > audio.MaxTargetLUFS {
		return fault.Fatal(fault.ClassAudio, "target-loudness", fmt.Sprintf("target %v LUFS out of range", o.TargetLUFS))
	}
	if o.TargetKbps <= 0 {
		return fault.Fatal(fault.ClassAudio, "target-kbps", "target bitrate must be positive")
	}
	if o.CeilingDBTP > 0 || o.CeilingDBTP < -20 {
		return fault.Fatal(fault.ClassAudio, "ceiling", fmt.Sprintf("true-peak ceiling %v dBTP out of range", o.CeilingDBTP))
	}
	if o.FramesPerPacket <= 0 || o.FramesPerPacket > 4800 {
		return fault.Fatal(fault.ClassAudio, "packet-frames", "frames per packet out of range")
	}
	if o.MaxSamples <= 0 {
		return fault.Fatal(fault.ClassLimit, "sample-cap", "sample cap must be positive")
	}
	return nil
}

// audioOut is the intermediate result of the PCM pipeline.
type audioOut struct {
	timeline      *clock.Timeline
	loudness      audio.LoudnessResult // measured before gain
	finalLoudness audio.LoudnessResult // measured after gain and limiting
	gain          audio.GainPlan
	truePeakDBTP  float64
	clipped       int64
	limited       int64
	framesOut     int64
	headPad       int
	tailPad       int
	accumError    int64
	pcmHash       string
	faults        []fault.Fault
}

// runAudio executes repair → read → resample → layout → loudness in order.
//
// The stream is spooled to a temporary file between the normalise and
// gain/limit stages.  This keeps peak memory to one block regardless of asset
// length, at the cost of one local temp file that is always removed.
func runAudio(asset *manifest.Asset, decl manifest.TrackDecl, ti *mediam.TrackInfo, scan *mediam.ScanResult, opts Options, maxOutFrames int64) (*audioOut, error) {
	out := &audioOut{}
	// The output timeline matches the scan timeline: same rate, same origin,
	// same gaps.  Resampling changes the sample count, not the presentation
	// time, so the manifest and packet plan stay aligned to the scan.
	out.timeline = scan.Timeline

	inRate := ti.SampleRate
	if inRate == 0 {
		inRate = decl.SampleRate
	}
	if inRate == 0 {
		return nil, fault.Fatal(fault.ClassAudio, "sample-rate", "audio track declares no sample rate").OnTrack(decl.ID)
	}
	fromLayout, err := layoutForChannels(ti.Channels)
	if err != nil {
		return nil, fault.Fatal(fault.ClassAudio, "layout-unknown", err.Error()).OnTrack(decl.ID).Because(err)
	}
	// The container's channel count is authoritative; a manifest mismatch is
	// recorded rather than silently accepted.
	if decl.Layout != "" && string(fromLayout) != decl.Layout {
		out.faults = append(out.faults, fault.New(fault.ClassAudio, "layout-mismatch",
			fmt.Sprintf("manifest layout %q disagrees with container channels %d; using %s", decl.Layout, ti.Channels, fromLayout)).OnTrack(decl.ID))
	}

	resampler, err := audio.NewResampler(inRate, opts.OutputRate, mustChannels(fromLayout))
	if err != nil {
		return nil, fault.Fatal(fault.ClassAudio, "resample", err.Error()).OnTrack(decl.ID).Because(err)
	}
	channelMap, err := audio.BuildChannelMap(fromLayout, opts.OutputLayout)
	if err != nil {
		return nil, fault.Fatal(fault.ClassAudio, "layout-map", err.Error()).OnTrack(decl.ID).Because(err)
	}
	outCh := mustChannels(opts.OutputLayout)
	inCh := mustChannels(fromLayout)

	spool, err := os.CreateTemp("", "mediad-pcm-*")
	if err != nil {
		return nil, fault.Fatal(fault.ClassInput, "spool", err.Error()).Because(err)
	}
	defer func() {
		spool.Close()
		os.Remove(spool.Name())
	}()

	// Stage 1+2: read → resample → layout, streaming into the spool.
	hop := int(math.Round(float64(opts.OutputRate) * audio.BlockHopSeconds))
	meter, err := audio.NewMeter(opts.OutputRate, opts.OutputLayout)
	if err != nil {
		return nil, err
	}
	spoolFrames := int64(0)
	writeSpool := func(frames []float64) error {
		spoolFrames += int64(len(frames) / outCh)
		if spoolFrames > maxOutFrames {
			return fault.Fatal(fault.ClassLimit, "output-frames",
				fmt.Sprintf("resampled stream exceeds the %d-frame cap", maxOutFrames)).OnTrack(decl.ID)
		}
		return writeFloatBlock(spool, frames)
	}
	// To feed the meter in hops without buffering the whole stream, accumulate
	// at most one block of leftovers in memory.
	var meterBuf []float64
	process := func(frames []float64) error {
		rs, err := resampler.Convert(frames)
		if err != nil {
			return err
		}
		lm, err := channelMap.Apply(rs)
		if err != nil {
			return err
		}
		meterBuf = append(meterBuf, lm...)
		stride := hop * outCh
		for len(meterBuf) >= stride {
			if err := meter.Feed(meterBuf[:stride]); err != nil {
				return err
			}
			meterBuf = meterBuf[stride:]
		}
		if err := writeSpool(lm); err != nil {
			return err
		}
		out.framesOut += int64(len(rs) / inCh)
		return nil
	}
	if _, err := readConfirmedPCM(asset.ResolvePath(decl), ti, process); err != nil {
		return nil, err
	}
	tail, err := resampler.Flush()
	if err != nil {
		return nil, err
	}
	lm, err := channelMap.Apply(tail)
	if err != nil {
		return nil, err
	}
	meterBuf = append(meterBuf, lm...)
	for len(meterBuf) >= hop*outCh {
		if err := meter.Feed(meterBuf[:hop*outCh]); err != nil {
			return nil, err
		}
		meterBuf = meterBuf[hop*outCh:]
	}
	if err := writeSpool(lm); err != nil {
		return nil, err
	}
	out.headPad, out.tailPad = resampler.EdgePolicy()
	// Report the carried conversion remainder in whole target ticks: a proper
	// fraction below one tick rounds up to at most 1.
	carry, den := resampler.ErrorTicksExact()
	if den > 0 && carry > 0 {
		out.accumError = 1
	} else {
		out.accumError = 0
	}
	out.loudness = meter.Integrated()

	// Stage 3: gain planning.
	out.gain, err = audio.PlanGain(out.loudness, opts.TargetLUFS, opts.CeilingDBTP)
	if err != nil {
		return nil, err
	}
	for _, note := range out.gain.Notes {
		out.faults = append(out.faults, fault.New(fault.ClassAudio, "loudness-note", note).OnTrack(decl.ID))
	}

	// Stage 4: read the spool back, apply gain and limiting, write the final
	// PCM hash as we go.
	if err := spool.Sync(); err != nil {
		return nil, err
	}
	if _, err := spool.Seek(0, 0); err != nil {
		return nil, err
	}
	ceilingLinear := math.Pow(10, opts.CeilingDBTP/20)
	limiter := audio.NewLimiter(ceilingLinear, out.gain.GainLinear)
	tpm, err := audio.NewTruePeakMeter(outCh)
	if err != nil {
		return nil, err
	}
	hasher, _ := newPCMHasher()
	finalMeter, err := audio.NewMeter(opts.OutputRate, opts.OutputLayout)
	if err != nil {
		return nil, err
	}
	var finalPending []float64
	finalFrames := int64(0)
	maxAbs := 0.0
	err = readFloatBlocks(spool, outCh, func(block []float64) error {
		processed := limiter.Process(block, outCh)
		if len(processed) == 0 {
			return fault.Fatal(fault.ClassAudio, "limiter", "limiter dropped a block")
		}
		if err := tpm.Feed(processed); err != nil {
			return err
		}
		hasher.write(processed)
		finalFrames += int64(len(processed) / outCh)
		for _, v := range processed {
			if math.Abs(v) > maxAbs {
				maxAbs = math.Abs(v)
			}
		}
		// Measure the signal that is actually forwarded, hop by hop.
		finalPending = append(finalPending, processed...)
		stride := hop * outCh
		for len(finalPending) >= stride {
			if err := finalMeter.Feed(finalPending[:stride]); err != nil {
				return err
			}
			finalPending = finalPending[stride:]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.finalLoudness = finalMeter.Integrated()
	out.clipped = limiter.ClippedSamples()
	out.framesOut = finalFrames
	out.truePeakDBTP = tpm.DBTP()
	if math.IsInf(out.truePeakDBTP, -1) {
		out.truePeakDBTP = -120
	}
	out.pcmHash = hasher.sum()
	if out.clipped > 0 {
		out.faults = append(out.faults, fault.New(fault.ClassAudio, "clipped",
			fmt.Sprintf("%d samples were hard-clipped by the limiter", out.clipped)).OnTrack(decl.ID))
	}
	if maxAbs > 1+1e-12 {
		return nil, fault.Fatal(fault.ClassAudio, "pcm-range",
			fmt.Sprintf("a sample reached %v outside the representable range", maxAbs)).OnTrack(decl.ID)
	}
	return out, nil
}

// layoutForChannels maps a container channel count to a supported layout.
func layoutForChannels(c int) (audio.Layout, error) {
	switch c {
	case 1:
		return audio.Mono, nil
	case 2:
		return audio.Stereo, nil
	case 6:
		return audio.Five1, nil
	default:
		return "", fmt.Errorf("audio: %d-channel containers have no known layout", c)
	}
}

func mustChannels(l audio.Layout) int {
	n, _ := l.Channels()
	return n
}
