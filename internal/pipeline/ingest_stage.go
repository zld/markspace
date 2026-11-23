package pipeline

import (
	"mediapipe/internal/fault"
	"mediapipe/internal/ingest"
	"mediapipe/internal/model"
	"mediapipe/internal/testdata"
)

// ingestAll parses each available container and attaches complete tracks to
// the timeline. Recoverable faults are collected; a failed container simply
// contributes no track rather than aborting the whole run under recovery.
func ingestAll(tl *model.Timeline, paths fixturePaths, opts Options,
	errs *fault.Batch) {
	mp4Tracks, mp4Err := ingest.ParseMP4(paths.mp4, errs)
	if mp4Err == nil {
		tl.Tracks = append(tl.Tracks, mp4Tracks...)
	}

	mxfTracks, mxfErr := ingest.ParseMXF(paths.mxf, errs)
	if mxfErr == nil {
		tl.Tracks = append(tl.Tracks, mxfTracks...)
	}

	clockRate := opts.ClockRate
	if clockRate == 0 {
		clockRate = 48000
	}
	rtpRes, rtpErr := ingest.ReadRTPCapture(paths.rtp, "rtp-audio-0",
		clockRate, errs)
	if rtpErr == nil && rtpRes != nil {
		tl.Tracks = append(tl.Tracks, rtpRes.Track)
		tl.Quarantined = rtpRes.Quarantined
		tl.WrapCount = rtpRes.Wraps
	}

	// Attach generated PCM to the MP4 audio track so silence analysis and the
	// audio fingerprint operate on the known deterministic waveform.
	attachPCM(tl, opts)
}

// attachPCM copies the fixture PCM onto the MP4 audio samples in fixed 1024-
// frame groups matching the writer, and onto RTP samples as 20ms frames.
func attachPCM(tl *model.Timeline, opts Options) {
	audioContent := optionsAudioPCM(opts)
	const group = 1024
	for _, tr := range tl.Tracks {
		if tr.Kind != model.KindAudio || tr.Container != model.ContainerMP4 {
			continue
		}
		for i := range tr.Samples {
			lo := i * group
			hi := lo + group
			if hi > len(audioContent) {
				hi = len(audioContent)
			}
			if lo < len(audioContent) {
				tr.Samples[i].Pcm = append([]int16(nil), audioContent[lo:hi]...)
			}
		}
	}
}

// optionsAudioPCM regenerates the exact fixture waveform deterministically.
func optionsAudioPCM(opts Options) []int16 {
	return regeneratePCM(opts.Seed)
}

// regeneratePCM rebuilds the same 4s/48kHz tone used by the MP4 fixture.
func regeneratePCM(seed int64) []int16 {
	return testdata.NewAudio(seed, 48000, 4_000_000_000).PCM
}
