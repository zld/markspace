package render

import (
	mp4scan "replayctl/internal/mp4"
	"replayctl/internal/timebase"
	"replayctl/internal/timeline"
)

type audioTransition struct {
	tr         timeline.Transition
	start, end int64
}

func (r *Renderer) sourceSample(tc tickClip, t int64) (int16, error) {
	src, err := r.sourceFor(tc.clip)
	if err != nil {
		return 0, err
	}
	pcm, err := src.PCM16()
	if err != nil {
		return 0, err
	}
	sourceTick := tc.sourceIn + (t - tc.start)
	if sourceTick < 0 || int(sourceTick) >= len(pcm) {
		return 0, faultSampleRange(src.Path, sourceTick)
	}
	return pcm[sourceTick], nil
}

func faultSampleRange(path string, tick int64) error {
	return errAudioRange(path, tick)
}

func (r *Renderer) renderAudio(tl timeline.Timeline, start, end int64) (mp4scan.TrackIn, int, int16, error) {
	count := int(end - start)
	out := make([]int16, 0, count)
	clips := make([]tickClip, 0, len(tl.Clips))
	for _, c := range tl.Clips {
		tc, err := clipTicks(c, timebase.AudioRate)
		if err != nil {
			return mp4scan.TrackIn{}, 0, 0, err
		}
		clips = append(clips, tc)
	}
	var transitions []audioTransition
	for _, tr := range tl.Transitions {
		s, e, err := tr.BoundsTicks(timebase.AudioRate)
		if err != nil {
			return mp4scan.TrackIn{}, 0, 0, err
		}
		transitions = append(transitions, audioTransition{tr: tr, start: s, end: e})
	}

	var peak int16
	for t := start; t < end; t++ {
		var active []tickClip
		for _, tc := range clips {
			if t >= tc.start && t < tc.end {
				active = append(active, tc)
			}
		}
		if len(active) == 0 {
			return mp4scan.TrackIn{}, 0, 0, errNoAudio(t)
		}

		var value int16
		var mixed bool
		for _, at := range transitions {
			if t < at.start || t >= at.end {
				continue
			}
			var from, to *tickClip
			for i := range active {
				if active[i].clip.ID == at.tr.FromID {
					from = &active[i]
				}
				if active[i].clip.ID == at.tr.ToID {
					to = &active[i]
				}
			}
			if from == nil || to == nil {
				return mp4scan.TrackIn{}, 0, 0, errTransitionMissing(t)
			}
			a, err := r.sourceSample(*from, t)
			if err != nil {
				return mp4scan.TrackIn{}, 0, 0, err
			}
			b, err := r.sourceSample(*to, t)
			if err != nil {
				return mp4scan.TrackIn{}, 0, 0, err
			}
			num, den := timeline.AlphaFraction(t, at.start, at.end)
			value = timeline.MixLinear(a, b, num, den)
			mixed = true
			break
		}
		if !mixed {
			var err error
			value, err = r.sourceSample(active[0], t)
			if err != nil {
				return mp4scan.TrackIn{}, 0, 0, err
			}
		}

		out = append(out, value)
		abs := int32(value)
		if abs < 0 {
			abs = -abs
		}
		if abs > int32(peak) {
			peak = int16(abs)
		}
	}

	data := make([]byte, len(out)*2)
	for i, v := range out {
		data[2*i] = byte(v)
		data[2*i+1] = byte(uint16(v) >> 8)
	}
	track := mp4scan.TrackIn{
		Kind: "audio", Codec: "sowt", Timescale: timebase.AudioRate,
		Channels: 1, Bits: 16, SampleRate: timebase.AudioRate,
		Data: data, DefaultSampleSize: 2, SampleCount: uint32(len(out)), Delta: 1,
	}
	return track, len(out), peak, nil
}
