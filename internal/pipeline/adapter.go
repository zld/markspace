package pipeline

import (
	"avbatch/internal/audio"
	"avbatch/internal/synth"
)

// FromGroup converts a generated capture group into a pipeline input. It lives
// here, next to Input, so the generator package has no dependency on the
// pipeline and tests in lower layers can import it freely.
func FromGroup(g *synth.Group) Input {
	in := Input{
		Init:       g.Init,
		TargetKbps: g.TargetKbps,
		Faults:     append([]string(nil), g.Faults...),
	}
	for _, f := range g.Fragments {
		in.Fragments = append(in.Fragments, FragmentInput{
			Ordinal:    f.Ordinal,
			Data:       f.Data,
			RTPPackets: f.RTPPackets,
			WallNanos:  f.WallNanos,
		})
	}
	in.AudioPCM = audio.PCM{
		SampleRate: g.Audio.SampleRate,
		Channels:   g.Audio.Channels,
		Frames:     g.Audio.Frames,
		StartNanos: g.Audio.StartNanos,
	}
	return in
}
