package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"

	"continuity/internal/manifest"
)

// Options controls sample generation.
type Options struct {
	Dir         string
	Seed        int64
	Constraints manifest.Constraints
}

// Generated records all sample paths and embedded defects.
type Generated struct {
	Dir               string            `json:"dir"`
	Seed              int64             `json:"seed"`
	Files             map[string]string `json:"files"`
	Defects           []Defect          `json:"defects"`
	NormalMKV         string            `json:"normal_mkv"`
	InterleavedMKV    string            `json:"interleaved_mkv"`
	UnknownLengthMKV  string            `json:"unknown_length_mkv"`
	RTPWrap           string            `json:"rtp_wrap"`
	EMSGMP4           string            `json:"emsg_mp4"`
	V1Manifest        string            `json:"v1_manifest"`
	ColorConflict     string            `json:"color_conflict_manifest"`
	CanonicalManifest string            `json:"canonical_manifest"`
}

// Generate creates every local sample required by tests and the demo.
func Generate(opts Options) (*Generated, error) {
	if opts.Seed == 0 {
		opts.Seed = 17
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	g := &Generated{Dir: opts.Dir, Seed: opts.Seed, Files: map[string]string{}}

	normal := BuildMKV(MKVSpec{Seed: opts.Seed, Clusters: 3})
	interleaved := BuildMKV(MKVSpec{Seed: opts.Seed + 1, Clusters: 4})
	unknown := BuildMKV(MKVSpec{Seed: opts.Seed + 2, Clusters: 3, UnknownSegment: true})
	if err := g.write("normal.mkv", normal); err != nil {
		return nil, err
	}
	if err := g.write("interleaved.mkv", interleaved); err != nil {
		return nil, err
	}
	if err := g.write("unknown-length.mkv", unknown); err != nil {
		return nil, err
	}
	g.NormalMKV = g.Files["normal.mkv"]
	g.InterleavedMKV = g.Files["interleaved.mkv"]
	g.UnknownLengthMKV = g.Files["unknown-length.mkv"]

	for _, sample := range []CorruptSample{TruncatedVINTSample(), OutOfBoundsBlockSample(), IllegalBoundarySample()} {
		if err := g.write(sample.Name, sample.Data); err != nil {
			return nil, err
		}
		sample.Defect.File = sample.Name
		g.Defects = append(g.Defects, sample.Defect)
	}

	rtpBytes, err := BuildRTPWrapRecords(opts.Seed)
	if err != nil {
		return nil, err
	}
	if err := g.write("rtp-wrap.bin", rtpBytes); err != nil {
		return nil, err
	}
	g.RTPWrap = g.Files["rtp-wrap.bin"]

	emsgBytes, err := BuildEMSGSwitchHint()
	if err != nil {
		return nil, err
	}
	if err := g.write("switch-hints.mp4", emsgBytes); err != nil {
		return nil, err
	}
	g.EMSGMP4 = g.Files["switch-hints.mp4"]

	if err := g.write("manifest-v1.json", []byte(v1ManifestJSON())); err != nil {
		return nil, err
	}
	g.V1Manifest = g.Files["manifest-v1.json"]

	conflict := ColorConflictManifest(opts.Constraints)
	data, err := json.MarshalIndent(conflict, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := g.write("color-conflict.json", data); err != nil {
		return nil, err
	}
	g.ColorConflict = g.Files["color-conflict.json"]

	canonical := CanonicalManifest(opts.Seed, opts.Constraints)
	data, err = json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := g.write("manifest-canonical.json", data); err != nil {
		return nil, err
	}
	g.CanonicalManifest = g.Files["manifest-canonical.json"]

	defectsJSON, err := json.MarshalIndent(g.Defects, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := g.write("defects.json", defectsJSON); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Generated) write(name string, data []byte) error {
	path := filepath.Join(g.Dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	g.Files[name] = path
	return nil
}

// CanonicalManifest returns the known-good manifest for generated MKV.
func CanonicalManifest(seed int64, c manifest.Constraints) *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion:   manifest.CanonicalVersion,
		Seed:            seed,
		SourceKind:      "mkv+rtp+emsg",
		TimecodeScaleNS: 1_000_000,
		Tracks: []manifest.TrackMeta{
			{
				ID: 1, Type: "video", Codec: "V_VP9", Timescale: 1000,
				Width: 640, Height: 360, SourceBitrateBPS: 3_200_000,
				FrameDurationNS: 40_000_000, DurationNS: 1_200_000_000,
			},
			{
				ID: 2, Type: "audio", Codec: "A_OPUS", Timescale: 1000,
				InputSampleRate: 44100, TargetSampleRate: 48000,
				Channels: 6, Layout: "5.1", LayoutKnown: true,
				FrameDurationNS: 20_000_000, DurationNS: 1_200_000_000,
			},
		},
		ColorObservations: []manifest.ColorObservation{
			{Field: "color_primaries", Value: "bt601", Level: manifest.ColorContainer, Source: "container:matroska-info"},
			{Field: "color_primaries", Value: "bt709", Level: manifest.ColorTrack, Source: "track:video-colour"},
			{Field: "color_primaries", Value: "bt709", Level: manifest.ColorFrame, Source: "frame:keyframe-vui"},
			{Field: "transfer_characteristics", Value: "bt709", Level: manifest.ColorTrack, Source: "track:video-colour"},
			{Field: "matrix_coefficients", Value: "bt709", Level: manifest.ColorFrame, Source: "frame:sps-vui"},
		},
		Constraints:   c,
		DefaultLFEMix: false,
	}
}

// ColorConflictManifest returns metadata with an unresolved same-priority clash.
func ColorConflictManifest(c manifest.Constraints) *manifest.Manifest {
	m := CanonicalManifest(17, c)
	m.ColorObservations = append(m.ColorObservations,
		manifest.ColorObservation{Field: "color_primaries", Value: "bt2020", Level: manifest.ColorFrame, Source: "frame:sei-mastering-display"},
	)
	m.ExplicitColor = nil
	return m
}

func v1ManifestJSON() string {
	return `{
  "manifest_version": 1,
  "clips": [
    {"id": 1, "type": "video", "codec": "V_VP9", "source_bitrate_bps": 3200000, "frame_duration_ns": 40000000},
    {"id": 2, "type": "audio", "codec": "A_OPUS", "rate": 44100, "ch": 6, "layout": "5.1"}
  ],
  "quality": {
    "coverage_min_ppm": 999000,
    "max_skew_ms": 20,
    "bitrate_cap_bps": 2400000,
    "processing_budget_ms": 2000
  }
}`
}
