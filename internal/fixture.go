package medpipe

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// FixtureNames centralizes the deterministic file names of a generated set.
type FixtureNames struct {
	SpecFile     string
	H264         string
	H265         string
	AV1          string
	Audio        string
	Truncated    string
	Rollback     string
	Conflict     string
	FatalDir     string
	FatalCapture string
	FatalSpec    string
}

// DefaultFixtureNames returns the conventional layout.
func DefaultFixtureNames() FixtureNames {
	return FixtureNames{
		SpecFile: "tracks.json", H264: "video_h264.rtp", H265: "video_h265.rtp",
		AV1: "video_av1.rtp", Audio: "audio_opus.rtp", Truncated: "truncated_h264.rtp",
		Rollback: "rollback_h265.rtp", Conflict: "conflict_av1.rtp",
		FatalDir: "fatal", FatalCapture: "ambiguous_h264.rtp", FatalSpec: "tracks.json",
	}
}

// FixtureParams controls the generated geometry. The defaults are tuned so
// the aggregate payload bitrate lands on the 4 Mbps acceptance target.
type FixtureParams struct {
	VideoFrames   int // per video track, 30 fps
	AudioFrames   int // 20 ms frames, 48 kHz
	VideoBytes    int // payload bytes per normal video frame
	AudioBytes    int // payload bytes per audio frame
	DegradedBytes int // payload bytes per degraded-track frame
}

// DefaultFixtureParams returns acceptance-tuned geometry. VideoBytes is
// calibrated so aggregate payload over the ~2 s window lands on 4 Mbps
// within the required 5% band.
func DefaultFixtureParams() FixtureParams {
	return FixtureParams{
		VideoFrames:   60,  // 2.0 s at 30 fps
		AudioFrames:   100, // 2.0 s at 50 fps
		VideoBytes:    4382,
		AudioBytes:    200,
		DegradedBytes: 500,
	}
}

const (
	fixtureVideoRate  uint32 = 90000
	fixtureVideoDelta uint64 = 3000
	fixtureAudioRate  uint32 = 48000
	fixtureAudioDelta uint64 = 960
)

// GenerateFixtures writes the complete fixture set into outDir and returns
// the track specs for the main batch and the fatal batch.
func GenerateFixtures(outDir string, p FixtureParams) (main, fatal []TrackSpec, err error) {
	n := DefaultFixtureNames()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Join(outDir, n.FatalDir), 0o755); err != nil {
		return nil, nil, err
	}

	video := func(file, track string, codec Codec) TrackSpec {
		return TrackSpec{File: file, Track: track, Codec: codec, Kind: KindVideo,
			ClockRate: fixtureVideoRate, Delta: fixtureVideoDelta}
	}
	main = []TrackSpec{
		video(n.H264, "video_h264", CodecH264), video(n.H265, "video_h265", CodecH265),
		video(n.AV1, "video_av1", CodecAV1), video(n.Truncated, "truncated_h264", CodecH264),
		video(n.Rollback, "rollback_h265", CodecH265), video(n.Conflict, "conflict_av1", CodecAV1),
		{File: n.Audio, Track: "audio_opus", Codec: CodecH264, Kind: KindAudio,
			ClockRate: fixtureAudioRate, Delta: fixtureAudioDelta,
			Audio: &AudioConfig{Track: "audio_opus", SampleRate: fixtureAudioRate, ChannelMap: StereoMap}},
	}
	// Delta 0: no trusted nominal step, so the duplicate cannot be repaired.
	fatal = []TrackSpec{{File: n.FatalCapture, Track: "ambiguous_h264", Codec: CodecH264,
		Kind: KindVideo, ClockRate: fixtureVideoRate, Delta: 0, FatalExpected: true}}

	type capture struct {
		dir, name string
		builder   func() *CaptureBuilder
	}
	captures := []capture{
		{outDir, n.H264, func() *CaptureBuilder { return buildNormalVideo(CodecH264, p) }},
		{outDir, n.H265, func() *CaptureBuilder { return buildNormalVideo(CodecH265, p) }},
		{outDir, n.AV1, func() *CaptureBuilder { return buildNormalVideo(CodecAV1, p) }},
		{outDir, n.Audio, func() *CaptureBuilder { return buildAudio(p) }},
		{outDir, n.Truncated, func() *CaptureBuilder { return buildTruncated(p) }},
		{outDir, n.Rollback, func() *CaptureBuilder { return buildRollback(p) }},
		{outDir, n.Conflict, func() *CaptureBuilder { return buildConflict(p) }},
		{filepath.Join(outDir, n.FatalDir), n.FatalCapture, func() *CaptureBuilder { return buildAmbiguous(p) }},
	}
	for _, c := range captures {
		if err = writeCapture(c.dir, c.name, c.builder()); err != nil {
			return
		}
	}
	if err = writeSpecs(outDir, n.SpecFile, main); err != nil {
		return
	}
	if err = writeSpecs(filepath.Join(outDir, n.FatalDir), n.FatalSpec, fatal); err != nil {
		return
	}
	return main, fatal, nil
}

func writeCapture(dir, name string, cb *CaptureBuilder) error {
	path, perr := ResolveLocal(dir, name)
	if perr != nil {
		return perr
	}
	return AtomicWriteFile(path, cb.buf.Bytes(), 0o644)
}

func writeSpecs(dir, name string, specs []TrackSpec) error {
	path, perr := ResolveLocal(dir, name)
	if perr != nil {
		return perr
	}
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(path, append(data, '\n'), 0o644)
}

// LoadSpecs reads a batch track specification file.
func LoadSpecs(dir string) ([]TrackSpec, error) {
	path, perr := ResolveLocal(dir, DefaultFixtureNames().SpecFile)
	if perr != nil {
		return nil, perr
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var specs []TrackSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, err
	}
	return specs, nil
}
