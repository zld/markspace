package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"regional.example/mediad/internal/manifest"
)

// Scenario names one generated case.
type Scenario string

const (
	// ScenarioNormal is a clean 48 kHz stereo file with MP4 fast-start.
	ScenarioNormal Scenario = "normal"
	// ScenarioTruncated is cut mid-mdat so the tail samples are lost.
	ScenarioTruncated Scenario = "truncated"
	// ScenarioCorrupt is a container whose chunk index overlaps and whose
	// sample size table is inconsistent.
	ScenarioCorrupt Scenario = "corrupt"
	// ScenarioSparse is a 44.1 kHz mono file with an empty edit list, a
	// non-zero start and an omitted stco index.
	ScenarioSparse Scenario = "sparse"
	// ScenarioSilent is digital silence, used to check gain guards.
	ScenarioSilent Scenario = "silent"
	// ScenarioHot is a clipped, too-loud master that needs peak limiting.
	ScenarioHot Scenario = "hot"
)

// AllScenarios lists the scenarios the demo generates.
var AllScenarios = []Scenario{ScenarioNormal, ScenarioTruncated, ScenarioCorrupt, ScenarioSparse, ScenarioSilent, ScenarioHot}

// Generated describes one written fixture and the facts tests may assert on.
type Generated struct {
	Scenario Scenario
	Dir      string
	Manifest string // manifest file path
	MP4Path  string
	SHA256   string // of the MP4 bytes as written
	// Facts about the nominal file before any damage was introduced.
	SampleRate int
	Channels   int
	Layout     string
	Frames     int // PCM frames in the intact file
	// Damage expectations used by tests to know what recovery must find.
	ExpectTruncated bool
	ExpectMissing   bool
	ExpectIndexGap  bool
	ExpectNonZero   bool
}

// Generate writes all scenarios under dir, deriving every random choice from
// seed.  Existing files are replaced atomically; the input paths are never
// reused for output by the demo itself.
func Generate(dir string, seed int64) ([]Generated, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("fixtures: create dir: %w", err)
	}
	var out []Generated
	for _, sc := range AllScenarios {
		g, err := generateOne(dir, seed, sc)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	// Aggregate manifest pointing at every scenario for graph tests.
	if err := writeIndexManifest(dir, seed, out); err != nil {
		return nil, err
	}
	return out, nil
}

// seededRand gives each scenario a disjoint deterministic stream.
func seededRand(seed int64, sc Scenario) *rand.Rand {
	offset := int64(0)
	switch sc {
	case ScenarioNormal:
		offset = 1
	case ScenarioTruncated:
		offset = 2
	case ScenarioCorrupt:
		offset = 3
	case ScenarioSparse:
		offset = 4
	case ScenarioSilent:
		offset = 5
	case ScenarioHot:
		offset = 6
	}
	return rand.New(rand.NewSource(seed*7919 + offset*104729))
}

func generateOne(dir string, seed int64, sc Scenario) (Generated, error) {
	rng := seededRand(seed, sc)
	g := Generated{Scenario: sc, Dir: dir}

	spec := audioFileSpec{
		movieTimescale:  48000,
		mediaTimescale:  48000,
		trackID:         1,
		sampleFrames:    1024,
		samplesPerChunk: 8,
		opts:            defaultOpts(),
	}
	var signal Signal
	var startTick int64
	var edits []manifest.EditDecl

	switch sc {
	case ScenarioNormal:
		g.SampleRate, g.Channels, g.Layout = 48000, 2, "stereo"
		g.Frames = 48000 * 3 // 3 seconds; padded below to whole samples
		signal = Signal{Freq: 440, Amplitude: 0.18, Noise: 0.004}
		spec.opts.movieBeforeMedia = true
	case ScenarioTruncated:
		g.SampleRate, g.Channels, g.Layout = 48000, 2, "stereo"
		g.Frames = 48000 * 2 // padded below to whole samples
		signal = Signal{Freq: 523, Amplitude: 0.2, Noise: 0.004}
		g.ExpectTruncated = true
		g.ExpectMissing = true
		// Cut at 60% of the file so several whole samples and one partial
		// sample are lost while the fast-start moov index stays readable.
		spec.opts.movieBeforeMedia = true
		spec.opts.truncateFraction = 0.6
	case ScenarioCorrupt:
		g.SampleRate, g.Channels, g.Layout = 48000, 2, "stereo"
		g.Frames = 48000 * 2 // padded below to whole samples
		signal = Signal{Freq: 330, Amplitude: 0.2, Noise: 0.004}
		spec.samplesPerChunk = 10
		spec.opts.corruptChunkIndex = 3
		spec.opts.corruptSampleIndex = 25
		g.ExpectIndexGap = true
	case ScenarioSparse:
		g.SampleRate, g.Channels, g.Layout = 44100, 1, "mono"
		g.Frames = 44100 * 3 // padded below to whole samples
		signal = Signal{Freq: 660, Amplitude: 0.22, Noise: 0.003}
		spec.movieTimescale = 44100
		spec.mediaTimescale = 44100
		spec.sampleFrames = 1024
		spec.samplesPerChunk = 16
		spec.syncEvery = 8
		spec.opts.omitChunkOffsets = true
		g.ExpectIndexGap = true
		g.ExpectNonZero = true
		startTick = 44100 // 1 second into the presentation
		// 0.25 s empty edit, then the full media, at the movie timescale.
		edits = []manifest.EditDecl{
			{MediaTime: -1, Duration: 11025},
			{MediaTime: 0, Duration: int64(g.Frames)},
		}
	case ScenarioSilent:
		g.SampleRate, g.Channels, g.Layout = 48000, 2, "stereo"
		g.Frames = 48000
		signal = Signal{Freq: 0}
	case ScenarioHot:
		g.SampleRate, g.Channels, g.Layout = 48000, 6, "5.1"
		g.Frames = 48000
		signal = Signal{Freq: 1000, Amplitude: 0.3, Noise: 0.002, ClippingAfterGain: 3}
	}

	// Pad the generated PCM up to a whole number of MP4 samples so a clean
	// container has no partial tail; frame count reported stays exact.
	samples := (g.Frames + spec.sampleFrames - 1) / spec.sampleFrames
	padded := samples * spec.sampleFrames

	spec.sampleRate = g.SampleRate
	spec.channels = g.Channels
	spec.bitDepth = 16
	for _, e := range edits {
		spec.edits = append(spec.edits, editSpec{
			mediaTime: e.MediaTime,
			duration:  e.Duration,
			empty:     e.MediaTime < 0,
		})
	}
	// 5.1 needs 6 channels which is not a valid "stereo" file; the layout
	// string above already matches the channel count.
	spec.pcm = signal.render(padded, g.Channels, g.SampleRate, g.Layout, rng)

	raw, err := buildAudioBytes(spec)
	if err != nil {
		return g, err
	}
	name := string(sc) + ".mp4"
	g.MP4Path = filepath.Join(dir, name)
	if err := writeAtomic(g.MP4Path, raw); err != nil {
		return g, err
	}
	g.SHA256 = sha256hex(raw)

	man := buildManifest(sc, g, startTick, edits)
	manPath := filepath.Join(dir, string(sc)+".json")
	data, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return g, err
	}
	if err := writeAtomic(manPath, data); err != nil {
		return g, err
	}
	g.Manifest = manPath
	// Re-parse through the real validator so fixtures cannot drift from the
	// format the library accepts.
	if _, err := manifest.ParseFile(manPath); err != nil {
		return g, fmt.Errorf("fixtures: generated manifest %s is invalid: %w", manPath, err)
	}
	return g, nil
}

func buildManifest(sc Scenario, g Generated, startTick int64, edits []manifest.EditDecl) manifest.Asset {
	deps := []uint32(nil)
	man := manifest.Asset{
		Version:        1,
		AssetID:        "seed-" + string(sc),
		MovieTimescale: uint32(g.SampleRate),
		StartTick:      startTick,
		Tracks: []manifest.TrackDecl{{
			ID:             1,
			Role:           manifest.RoleAudio,
			Codec:          manifest.CodecPCM16,
			Required:       true,
			Path:           string(sc) + ".mp4",
			SampleRate:     g.SampleRate,
			Channels:       g.Channels,
			Layout:         g.Layout,
			MediaTimescale: uint32(g.SampleRate),
			Edits:          edits,
			DependsOn:      deps,
		}},
	}
	// The sparse case declares a video track it does not provide, so the
	// graph must degrade gracefully instead of failing the whole asset.
	if sc == ScenarioSparse {
		man.Tracks = append(man.Tracks, manifest.TrackDecl{
			ID:        2,
			Role:      manifest.RoleVideo,
			Codec:     "avc1",
			Required:  false,
			Path:      "sparse-video.mp4",
			DependsOn: []uint32{1},
		})
	}
	// A missing optional track reference exercises degraded planning.
	if sc == ScenarioCorrupt {
		man.Tracks = append(man.Tracks, manifest.TrackDecl{
			ID:        3,
			Role:      manifest.RoleVideo,
			Codec:     "avc1",
			Required:  false,
			Path:      filepath.Join("missing", "optional.mp4"),
			DependsOn: []uint32{1},
		})
	}
	return man
}

func writeIndexManifest(dir string, seed int64, gens []Generated) error {
	type entry struct {
		Scenario string `json:"scenario"`
		Manifest string `json:"manifest"`
		MP4      string `json:"mp4"`
		SHA256   string `json:"sha256"`
	}
	var list []entry
	for _, g := range gens {
		list = append(list, entry{
			Scenario: string(g.Scenario),
			Manifest: filepath.Base(g.Manifest),
			MP4:      filepath.Base(g.MP4Path),
			SHA256:   g.SHA256,
		})
	}
	idx := map[string]any{"seed": seed, "scenarios": list}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "index.json"), data)
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
