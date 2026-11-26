package fixture

import (
	"os"
	"path/filepath"

	"assetcheck/internal/ebml"
	"assetcheck/internal/manifest"
	"assetcheck/internal/media"
)

const (
	SampleRate     = 48000
	Channels       = 1
	BitsPerSample  = 16
	BlockSamples   = 480 // 10 ms of mono 16-bit PCM
	BlocksPerTrack = 100 // 1.0 second
	ClusterMS      = 100
)

type CueSpec struct {
	ID         string
	StartMS    int64
	DurationMS uint32
	English    string
	Chinese    string
}

func DefaultCues() []CueSpec {
	return []CueSpec{
		{ID: "cue-001", StartMS: 0, DurationMS: 200, English: "Ready?", Chinese: "准备好了吗？"},
		{ID: "cue-002", StartMS: 250, DurationMS: 200, English: "Follow me.", Chinese: "跟我来。"},
		{ID: "cue-003", StartMS: 500, DurationMS: 200, English: "The gate is open.", Chinese: "大门开了。"},
		{ID: "cue-004", StartMS: 750, DurationMS: 200, English: "We must hurry.", Chinese: "我们得快点。"},
	}
}

func RequiredLanguages() []string { return []string{"en", "zh"} }

func ExpectedCues(cues []CueSpec) []manifest.ExpectedCue {
	out := make([]manifest.ExpectedCue, 0, len(cues))
	for _, c := range cues {
		out = append(out, manifest.ExpectedCue{
			CueID: c.ID,
			Start: media.MustRat(c.StartMS, 1000),
			End:   media.MustRat(c.StartMS+int64(c.DurationMS), 1000),
		})
	}
	return out
}

type Options struct {
	AssetID        string
	WritingApp     string
	MuxingApp      string
	UnknownSegment bool
	InvalidVINT    bool
	Cues           []CueSpec
}

func DefaultOptions() Options {
	return Options{
		AssetID:    "PWF-CLEAN-001",
		WritingApp: "assetcheck-fixture-v1",
		MuxingApp:  "assetcheck-muxer-v1",
		Cues:       DefaultCues(),
	}
}

func Build(opts Options) ([]byte, error) {
	if opts.AssetID == "" {
		opts.AssetID = DefaultOptions().AssetID
	}
	if opts.WritingApp == "" {
		opts.WritingApp = "assetcheck-fixture-v1"
	}
	if opts.MuxingApp == "" {
		opts.MuxingApp = "assetcheck-muxer-v1"
	}
	if opts.Cues == nil {
		opts.Cues = DefaultCues()
	}

	header := buildHeader()
	info := buildInfo(opts)
	tracks := buildTracks()
	clusters, err := buildClusters(opts)
	if err != nil {
		return nil, err
	}
	segmentPayload := append([]byte(nil), info...)
	segmentPayload = append(segmentPayload, tracks...)
	segmentPayload = append(segmentPayload, clusters...)

	var segment []byte
	if opts.UnknownSegment {
		unknown, err := ebml.EncodeUnknownSize(8)
		if err != nil {
			return nil, err
		}
		segment = append(segment, ebml.EncodeID(ebml.IDSegment)...)
		segment = append(segment, unknown...)
		segment = append(segment, segmentPayload...)
	} else {
		segment = element(ebml.IDSegment, segmentPayload)
	}
	return append(header, segment...), nil
}

func buildHeader() []byte {
	var payload []byte
	payload = append(payload, uintElement(ebml.IDVersion, 1)...)
	payload = append(payload, uintElement(ebml.IDReadVersion, 1)...)
	payload = append(payload, stringElement(ebml.IDDocType, "matroska")...)
	return element(ebml.IDEBMLHeader, payload)
}

func buildInfo(opts Options) []byte {
	var payload []byte
	payload = append(payload, uintElement(ebml.IDTimecodeScale, 1_000_000)...)
	payload = append(payload, stringElement(ebml.IDMuxingApp, opts.MuxingApp)...)
	payload = append(payload, stringElement(ebml.IDWritingApp, opts.WritingApp)...)
	return element(ebml.IDInfo, payload)
}

func buildTracks() []byte {
	configs := []TrackConfig{
		{Number: 1, Type: ebml.TrackTypeAudio, Language: "en", CodecID: ebml.CodecPCM, Channels: Channels, Sampling: SampleRate, BitDepth: BitsPerSample},
		{Number: 2, Type: ebml.TrackTypeAudio, Language: "zh", CodecID: ebml.CodecPCM, Channels: Channels, Sampling: SampleRate, BitDepth: BitsPerSample},
		{Number: 3, Type: ebml.TrackTypeSubtitle, Language: "en", CodecID: ebml.CodecCue},
		{Number: 4, Type: ebml.TrackTypeSubtitle, Language: "zh", CodecID: ebml.CodecCue},
		{Number: 5, Type: ebml.TrackTypeEffect, Language: "", CodecID: ebml.CodecEffect},
	}
	var entries []byte
	for _, cfg := range configs {
		entries = append(entries, trackElement(cfg)...)
	}
	return element(ebml.IDTracks, entries)
}

func cueAt(cues []CueSpec, ms int64) *CueSpec {
	for i := range cues {
		if cues[i].StartMS == ms {
			return &cues[i]
		}
	}
	return nil
}

func effectAt(ms int64) (string, uint32, bool) {
	switch ms {
	case 120:
		return "sword-hit", 80, true
	case 640:
		return "gate-rumble", 160, true
	default:
		return "", 0, false
	}
}

func buildClusters(opts Options) ([]byte, error) {
	var out []byte
	for cluster := 0; cluster < BlocksPerTrack/10; cluster++ {
		baseMS := int64(cluster * ClusterMS)
		var payload []byte
		payload = append(payload, uintElement(ebml.IDTimecode, uint64(baseMS))...)

		for tick := 0; tick < 10; tick++ {
			ms := baseMS + int64(tick*10)
			relative := int16(ms - baseMS)

			if cue := cueAt(opts.Cues, ms); cue != nil {
				en, err := ebml.EncodeCuePayload(cue.ID, cue.English, cue.DurationMS)
				if err != nil {
					return nil, err
				}
				zh, err := ebml.EncodeCuePayload(cue.ID, cue.Chinese, cue.DurationMS)
				if err != nil {
					return nil, err
				}
				payload = append(payload, simpleBlock(3, relative, 0, en)...)
				payload = append(payload, simpleBlock(4, relative, 0, zh)...)
			}
			if name, dur, ok := effectAt(ms); ok {
				fx, err := ebml.EncodeEffectPayload(name, dur)
				if err != nil {
					return nil, err
				}
				payload = append(payload, simpleBlock(5, relative, 0, fx)...)
			}

			sampleOffset := (ms / 10) * BlockSamples
			pcm := pcmBlock(sampleOffset, BlockSamples)
			payload = append(payload, simpleBlock(1, relative, 0, pcm)...)
			payload = append(payload, simpleBlock(2, relative, 0, pcm)...)
		}

		if opts.InvalidVINT && cluster == 0 {
			// Valid element ID followed by a VINT first byte with no marker.
			payload = append(payload, ebml.EncodeID(ebml.IDSimpleBlock)...)
			payload = append(payload, 0x00)
		}
		out = append(out, element(ebml.IDCluster, payload)...)
	}
	return out, nil
}

func WriteFile(path string, opts Options) error {
	b, err := Build(opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWrite(path, b)
}
