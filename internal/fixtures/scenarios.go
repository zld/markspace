// Package fixtures builds deterministic MP4, subtitle, timecode and RTP
// fixtures for the three acceptance scenarios. Everything is generated in Go;
// nothing is read from the network or from pre-existing sample files.
package fixtures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediaaudit/internal/model"
)

// Scenario names.
const (
	ScenarioNormal = "normal"
	ScenarioEdge   = "edge"
	ScenarioBroken = "broken"
)

// Manifest records what Generate produced so the CLI can drive a run without
// guessing file names.
type Manifest struct {
	Dir          string            `json:"dir"`
	MP4          string            `json:"mp4"`
	Subtitles    map[string]string `json:"subtitles"`
	Timecodes    string            `json:"timecodes"`
	TimecodeMode string            `json:"timecode_mode"`
	RTP          map[string]string `json:"rtp"`
	Notes        []string          `json:"notes"`
}

// Scenarios are the three documented fixture sets.
func Scenarios() []string { return []string{ScenarioNormal, ScenarioEdge, ScenarioBroken} }

// Generate writes all three scenarios under root and returns their manifests
// keyed by scenario name at the top level.
func Generate(root string) (map[string]Manifest, error) {
	out := make(map[string]Manifest, 3)
	for _, name := range Scenarios() {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		m, err := GenerateScenario(dir, name)
		if err != nil {
			return nil, fmt.Errorf("fixtures: scenario %s: %w", name, err)
		}
		if err := SaveManifest(m); err != nil {
			return nil, fmt.Errorf("fixtures: scenario %s: %w", name, err)
		}
		out[name] = m
	}
	return out, nil
}

// GenerateScenario writes one scenario into dir.
func GenerateScenario(dir, name string) (Manifest, error) {
	switch name {
	case ScenarioNormal:
		return generateNormal(dir)
	case ScenarioEdge:
		return generateEdge(dir)
	case ScenarioBroken:
		return generateBroken(dir)
	default:
		return Manifest{}, fmt.Errorf("unknown scenario %q", name)
	}
}

// writeFile is a small helper that reports the path on failure.
func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// videoSpec is the shared 25 fps, 40-sample clip used by normal and edge.
func videoSpec(label string) MP4Spec {
	sizes := make([]uint32, 40)
	deltas := make([]uint32, 40)
	for i := range sizes {
		sizes[i] = uint32(64 + (i*37)%160)
		deltas[i] = 1200 // 1200/30000 = 1/25 s
	}
	keys := make([]int, 0, 40)
	for i := 1; i <= 40; i++ {
		keys = append(keys, i)
	}
	return MP4Spec{
		TrackID: 1, Timescale: 30000, Width: 64, Height: 36,
		Sizes: sizes, Deltas: deltas, Keyframes: keys, SamplesPerChunk: 4,
		Label: label,
	}
}

func generateNormal(dir string) (Manifest, error) {
	m := Manifest{Dir: dir, Subtitles: map[string]string{}, RTP: map[string]string{},
		TimecodeMode: "25"}
	spec := videoSpec("normal")
	mp4Path := filepath.Join(dir, "video.mp4")
	data, err := MP4(spec)
	if err != nil {
		return m, err
	}
	if err := writeFile(mp4Path, data); err != nil {
		return m, err
	}
	m.MP4 = mp4Path

	srt := strings.Join([]string{
		"1", "00:00:00,120 --> 00:00:00,480", "Opening line", "",
		"2", "00:00:00,600 --> 00:00:01,000", "Second line", "",
		"3", "00:00:01,100 --> 00:00:01,400", "Third line", "",
	}, "\r\n")
	srtPath := filepath.Join(dir, "captions.srt")
	if err := writeFile(srtPath, []byte(srt)); err != nil {
		return m, err
	}
	m.Subtitles["s1"] = srtPath

	vtt := "WEBVTT\n\n00:00:00.200 --> 00:00:00.520\nfirst\n\n00:00:01.000 --> 00:00:01.400\nsecond\n"
	vttPath := filepath.Join(dir, "captions.vtt")
	if err := writeFile(vttPath, []byte(vtt)); err != nil {
		return m, err
	}
	m.Subtitles["s2"] = vttPath

	tc := "# 25 fps programme clock\n00:00:00:00\n00:00:00:12\n00:00:01:00\n00:00:01:12\n"
	tcPath := filepath.Join(dir, "programme.tc")
	if err := writeFile(tcPath, []byte(tc)); err != nil {
		return m, err
	}
	m.Timecodes = tcPath

	rtpPath := filepath.Join(dir, "audio.rtp")
	rtp, err := RTPStream(RTPSpec{
		Timescale: 90000, RefTimescale: 90000, Count: 40,
		StartTS: 1000, StepTS: 3600, StepRef: 3600,
	})
	if err != nil {
		return m, err
	}
	if err := writeFile(rtpPath, rtp); err != nil {
		return m, err
	}
	m.RTP["a1"] = rtpPath
	m.Notes = append(m.Notes, "clean 25 fps clip, aligned captions, no drift")
	return m, nil
}

func generateEdge(dir string) (Manifest, error) {
	m := Manifest{Dir: dir, Subtitles: map[string]string{}, RTP: map[string]string{},
		TimecodeMode: "25"}
	spec := videoSpec("edge")
	mp4Path := filepath.Join(dir, "video.mp4")
	data, err := MP4(spec)
	if err != nil {
		return m, err
	}
	if err := writeFile(mp4Path, data); err != nil {
		return m, err
	}
	m.MP4 = mp4Path

	// Cue 2 spans a frame cut (0.36 -> 0.72 crosses cuts at 0.40, 0.48, ...).
	srt := strings.Join([]string{
		"1", "00:00:00,100 --> 00:00:00,300", "before cut", "",
		"2", "00:00:00,360 --> 00:00:00,760", "spans a cut", "",
		"3", "00:00:01,200 --> 00:00:01,200", "zero length", "",
		"4", "00:00:01,800 --> 00:00:01,600", "reversed", "",
		"5", "00:00:01,900 --> 00:00:02,200", "overlap A", "",
		"6", "00:00:02,100 --> 00:00:02,400", "overlap B", "",
	}, "\n")
	srtPath := filepath.Join(dir, "captions.srt")
	if err := writeFile(srtPath, []byte(srt)); err != nil {
		return m, err
	}
	m.Subtitles["s1"] = srtPath

	// Invalid DF frame labels: 00:01:00;00 and ;01 are dropped by the format.
	tc := strings.Join([]string{
		"# 29.97 drop-frame, including illegal dropped labels",
		"00:00:00;00",
		"00:00:00;29",
		"00:01:00;00",
		"00:01:00;01",
		"00:01:00;02",
		"00:02:00;00",
	}, "\n")
	tcPath := filepath.Join(dir, "programme.tc")
	if err := writeFile(tcPath, []byte(tc)); err != nil {
		return m, err
	}
	m.Timecodes = tcPath
	m.TimecodeMode = "29.97df"

	// One stream starts just below 2^32 and wraps; the other drifts 2000 ppm
	// fast, which must fail only its own track.
	wrapPath := filepath.Join(dir, "wrap.rtp")
	wrap, err := RTPStream(RTPSpec{
		Timescale: 90000, RefTimescale: 90000, Count: 30,
		StartTS: 4294967295 - 3600*2, StepTS: 3600, StepRef: 3600,
	})
	if err != nil {
		return m, err
	}
	if err := writeFile(wrapPath, wrap); err != nil {
		return m, err
	}
	m.RTP["a1-wrap"] = wrapPath

	driftPath := filepath.Join(dir, "drift.rtp")
	drift, err := RTPStream(RTPSpec{
		Timescale: 90000, RefTimescale: 90000, Count: 40,
		StartTS: 500, StepTS: 10000, StepRef: 10000, DriftPPM: 2000,
	})
	if err != nil {
		return m, err
	}
	if err := writeFile(driftPath, drift); err != nil {
		return m, err
	}
	m.RTP["a2-drift"] = driftPath
	m.Notes = append(m.Notes, "invalid DF labels, wrapped RTP timestamps, 2000 ppm drift, cut-spanning and malformed cues")
	return m, nil
}

func generateBroken(dir string) (Manifest, error) {
	m := Manifest{Dir: dir, Subtitles: map[string]string{}, RTP: map[string]string{},
		TimecodeMode: "25"}
	spec := videoSpec("broken")
	spec.TruncateSample = 80
	data, err := MP4(spec)
	if err != nil {
		return m, err
	}
	// Damage the keyframe index in place: readers must fall back to scanning
	// and report the bad entry plus its byte offset.
	corrupted, badIndexOffset, err := CorruptIndexBytes(data)
	if err != nil {
		return m, err
	}
	m.Notes = append(m.Notes, fmt.Sprintf("damaged stss entry at byte %d", badIndexOffset))
	mp4Path := filepath.Join(dir, "video.mp4")
	if err := writeFile(mp4Path, corrupted); err != nil {
		return m, err
	}
	m.MP4 = mp4Path

	srt := strings.Join([]string{
		"1", "00:00:00,000 --> 00:00:00,500", "still readable", "",
	}, "\n")
	srtPath := filepath.Join(dir, "captions.srt")
	if err := writeFile(srtPath, []byte(srt)); err != nil {
		return m, err
	}
	m.Subtitles["s1"] = srtPath

	tc := "00:00:00:00\n00:00:01:00\n"
	tcPath := filepath.Join(dir, "programme.tc")
	if err := writeFile(tcPath, []byte(tc)); err != nil {
		return m, err
	}
	m.Timecodes = tcPath
	m.Notes = append(m.Notes, "truncated mdat plus corrupted keyframe index")
	return m, nil
}

// SpanOf is a convenience used by tests to describe fixture timing.
func SpanOf(startMS, endMS int64) model.Span {
	return model.Span{
		Start: model.MustRat(startMS, 1000),
		End:   model.MustRat(endMS, 1000),
	}
}

// ManifestName is the per-scenario descriptor file.
const ManifestName = "fixtures.json"

// SaveManifest writes the scenario descriptor next to its fixtures.
func SaveManifest(m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(m.Dir, ManifestName), append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// LoadManifest reads a scenario descriptor.
func LoadManifest(dir string) (Manifest, error) {
	var m Manifest
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.MP4 == "" {
		return m, fmt.Errorf("%s does not describe an MP4", path)
	}
	return m, nil
}
