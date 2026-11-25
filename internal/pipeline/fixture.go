// Package pipeline wires the media kernel modules into the deterministic demo.
package pipeline

import (
	"math"

	"mediapack/internal/clock"
	"mediapack/internal/index"
	"mediapack/internal/stream"
	"mediapack/internal/subtitle"
)

const (
	SegmentCount       = 3
	SegmentDuration    = clock.Ticks(2 * clock.TicksPerSecond)
	CorruptLayer       = "ios/720p"
	CorruptSegmentZero = 1 // zero-based; reported as segment 2
)

// Rendition is one bitrate/geometry representation.
type Rendition struct {
	Name        string `json:"name"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	BitrateKbps int    `json:"bitrate_kbps"`
	Seed        byte   `json:"seed"`
}

// PlatformProfile contains target-specific encapsulation parameters.
type PlatformProfile struct {
	Name         string `json:"name"`
	FragmentMS   int    `json:"fragment_ms"`
	MP4Brand     string `json:"mp4_brand"`
	RTPClockRate int    `json:"rtp_clock_rate"`
}

// BuildParams is canonicalized into the asset params digest.
type BuildParams struct {
	Platform      string `json:"platform"`
	Rendition     string `json:"rendition"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	BitrateKbps   int    `json:"bitrate_kbps"`
	FragmentTicks int64  `json:"fragment_ticks"`
	OutputFPS     int    `json:"output_fps"`
	Timescale     int64  `json:"timescale"`
	RTPClockRate  int    `json:"rtp_clock_rate"`
	MP4Brand      string `json:"mp4_brand"`
}

// Fixture contains all generated sample input data.
type Fixture struct {
	SourceSHA256 string
	SRT          []byte
	VTT          []byte
	Cues         []subtitle.Cue
	Renditions   []Rendition
	Platforms    []PlatformProfile
	Windows      []stream.Window
	OutputRate   clock.Rate
	Frame        []byte
	Audio        []float64
}

// NewFixture generates deterministic game release inputs in memory.
func NewFixture() (*Fixture, error) {
	srt := []byte(`1
00:00:01,000 --> 00:00:03,500
Crossing the first fragment boundary

2
00:00:04,000 --> 00:00:05,250
Second cue after boundary
`)
	vtt := []byte(`WEBVTT

00:00:01.000 --> 00:00:03.500
Crossing the first fragment boundary

00:00:04.000 --> 00:00:05.250
Second cue after boundary
`)
	srtDoc, err := subtitle.ParseSRT("game-dialogue.srt", srt)
	if err != nil {
		return nil, err
	}
	if _, err := subtitle.ParseVTT("game-dialogue.vtt", vtt); err != nil {
		return nil, err
	}
	if err := validateTimecodeFixtures(); err != nil {
		return nil, err
	}

	windows := make([]stream.Window, SegmentCount)
	for i := range windows {
		windows[i] = stream.Window{Index: i, Start: clock.Ticks(int64(i) * int64(SegmentDuration)), End: clock.Ticks(int64(i+1) * int64(SegmentDuration))}
	}
	frame := make([]byte, 256)
	for i := range frame {
		frame[i] = byte(i)
	}
	audio := make([]float64, 1024)
	for i := range audio {
		audio[i] = 0.7 * math.Sin(2*math.Pi*440*float64(i)/48000)
	}

	fx := &Fixture{
		SourceSHA256: index.SHA256Hex(append(append([]byte("game-release-fixture-v1\n"), srt...), vtt...)),
		SRT:          srt,
		VTT:          vtt,
		Cues:         srtDoc.Cues,
		Renditions: []Rendition{
			{Name: "1080p", Width: 1920, Height: 1080, BitrateKbps: 8000, Seed: 17},
			{Name: "720p", Width: 1280, Height: 720, BitrateKbps: 4500, Seed: 41},
			{Name: "480p", Width: 854, Height: 480, BitrateKbps: 1800, Seed: 73},
		},
		Platforms: []PlatformProfile{
			{Name: "ios", FragmentMS: 2000, MP4Brand: "mp42", RTPClockRate: 90000},
			{Name: "android", FragmentMS: 2000, MP4Brand: "mp42", RTPClockRate: 90000},
			{Name: "web", FragmentMS: 2000, MP4Brand: "iso5", RTPClockRate: 90000},
		},
		Windows:    windows,
		OutputRate: clock.Rate25,
		Frame:      frame,
		Audio:      audio,
	}
	return fx, nil
}

func validateTimecodeFixtures() error {
	cases := []struct {
		rate clock.Rate
		code string
	}{
		{clock.Rate24, "01:02:03:04"},
		{clock.Rate25, "01:02:03:04"},
		{clock.Rate2997ND, "01:02:03:04"},
		{clock.Rate2997DF, "01:02:03;04"},
		{clock.Rate50, "01:02:03:04"},
	}
	for _, tc := range cases {
		if _, err := clock.ParseTimecode("generated-timecodes", 0, tc.rate, tc.code); err != nil {
			return err
		}
	}
	return nil
}

// LayerNames returns deterministic platform/rendition layer names.
func (fx *Fixture) LayerNames() []string {
	var out []string
	for _, p := range fx.Platforms {
		for _, r := range fx.Renditions {
			out = append(out, p.Name+"/"+r.Name)
		}
	}
	return out
}

// Specs builds the content/params/platform index identities.
func (fx *Fixture) Specs() ([]index.Spec, map[string]BuildParams, error) {
	var specs []index.Spec
	paramsByAsset := map[string]BuildParams{}
	for _, p := range fx.Platforms {
		for _, r := range fx.Renditions {
			params := BuildParams{
				Platform: p.Name, Rendition: r.Name, Width: r.Width, Height: r.Height,
				BitrateKbps: r.BitrateKbps, FragmentTicks: int64(SegmentDuration),
				OutputFPS: fx.OutputRate.NominalFPS, Timescale: clock.TicksPerSecond,
				RTPClockRate: p.RTPClockRate, MP4Brand: p.MP4Brand,
			}
			ps, err := index.ParamsDigest(params)
			if err != nil {
				return nil, nil, err
			}
			id := "mp4:" + p.Name + "/" + r.Name
			specs = append(specs, index.Spec{ID: id, Key: index.Key{
				SourceSHA256: fx.SourceSHA256, ParamsSHA256: ps, Platform: p.Name,
			}})
			paramsByAsset[id] = params
		}
	}
	for _, format := range []string{"srt", "vtt"} {
		ps, err := index.ParamsDigest(map[string]string{"format": format, "subsystem": "subtitle"})
		if err != nil {
			return nil, nil, err
		}
		specs = append(specs, index.Spec{ID: "subtitle:" + format, Key: index.Key{
			SourceSHA256: fx.SourceSHA256, ParamsSHA256: ps, Platform: "all",
		}})
	}
	return specs, paramsByAsset, nil
}

// IdentityDigest hashes the complete set of asset identities for checkpoints.
func IdentityDigest(specs []index.Spec) string {
	keys := make([]index.Key, 0, len(specs))
	for _, s := range specs {
		keys = append(keys, s.Key)
	}
	b, _ := index.CanonicalJSON(keys)
	return index.SHA256Hex(b)
}
