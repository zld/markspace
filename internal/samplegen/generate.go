package samplegen

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"media-archiver/internal/media"
	"media-archiver/internal/wav"
)

const (
	DemoSampleRate    = 48000
	DemoChannels      = 2
	DemoBits          = 16
	DemoBlockFrames   = 240
	DemoLookupOffset  = 60
	DemoBlockCount    = 12
	TruncateTailBytes = 3
)

// Options controls deterministic fixture generation.
type Options struct {
	Seed       int64
	Injection  string // none, truncate, backward, duplicate
	OutputPath string
}

// Expected records positions the acceptance checks can compare against.
type Expected struct {
	Seed                int64  `json:"seed"`
	Injection           string `json:"injection"`
	Path                string `json:"path"`
	SampleRate          int    `json:"sample_rate"`
	Channels            int    `json:"channels"`
	BitsPerSample       int    `json:"bits_per_sample"`
	BlockFrames         int    `json:"block_frames"`
	BlockCount          int    `json:"block_count"`
	TargetFrame         int64  `json:"target_frame"`
	ExpectedBlock       int    `json:"expected_block"`
	ExpectedFrameOffset int64  `json:"expected_frame_offset"`
	CorruptOffset       int64  `json:"corrupt_offset,omitempty"`
	BackwardBlock       int    `json:"backward_block,omitempty"`
	OriginalTSNS        int64  `json:"original_ts_ns,omitempty"`
	RepairedTSNS        int64  `json:"repaired_ts_ns,omitempty"`
	DuplicateBlock      int    `json:"duplicate_block,omitempty"`
}

type Generated struct {
	Path     string
	Expected Expected
}

// Generate creates a deterministic multi-block PCM WAV sample.
func Generate(opts Options) (*Generated, error) {
	if opts.OutputPath == "" {
		return nil, media.NewError(media.CodeInvalidConfig, media.StageParse, -1, "sample output path required", nil)
	}
	injection := opts.Injection
	if injection == "" {
		injection = "none"
	}
	format := media.PCMFormat{
		AudioFormat:   1,
		Channels:      DemoChannels,
		SampleRate:    DemoSampleRate,
		ByteRate:      DemoSampleRate * DemoChannels * DemoBits / 8,
		BlockAlign:    DemoChannels * DemoBits / 8,
		BitsPerSample: DemoBits,
	}
	tb := media.Timebase{SampleRate: DemoSampleRate}
	rng := rand.New(rand.NewSource(opts.Seed))

	spec := WAVSpec{Format: format}
	for b := 0; b < DemoBlockCount; b++ {
		spec.Blocks = append(spec.Blocks, BlockSpec{
			Timestamp: &TimestampChunk{Present: true, NS: tb.FramesToNS(int64(b * DemoBlockFrames))},
			PCM:       makeBlockPCM(opts.Seed, b, rng),
		})
	}

	exp := Expected{
		Seed: opts.Seed, Injection: injection, Path: opts.OutputPath,
		SampleRate: DemoSampleRate, Channels: DemoChannels, BitsPerSample: DemoBits,
		BlockFrames: DemoBlockFrames, TargetFrame: 6*DemoBlockFrames + DemoLookupOffset,
		ExpectedBlock: 6, ExpectedFrameOffset: DemoLookupOffset,
	}

	switch injection {
	case "none", "normal":
	case "backward":
		block := 6
		exp.BackwardBlock = block
		exp.OriginalTSNS = tb.FramesToNS(2 * DemoBlockFrames)
		exp.RepairedTSNS = tb.FramesToNS(int64(block * DemoBlockFrames))
		spec.Blocks[block].Timestamp.NS = exp.OriginalTSNS
	case "duplicate":
		src := 3
		dup := spec.Blocks[src]
		dup.Timestamp = &TimestampChunk{Present: true, NS: spec.Blocks[src].Timestamp.NS}
		spec.Blocks = insertBlock(spec.Blocks, src+1, dup)
		exp.DuplicateBlock = src + 1
		// Insertion shifts the original block six from index 6 to index 7.
		exp.TargetFrame = 7*DemoBlockFrames + DemoLookupOffset
		exp.ExpectedBlock = 7
		exp.ExpectedFrameOffset = DemoLookupOffset
	case "truncate":
		// Handled after encoding so the exact source offset can be read back.
	default:
		return nil, media.NewError(media.CodeInvalidConfig, media.StageParse, -1, "unknown injection: "+injection, nil)
	}

	if err := WriteWAV(opts.OutputPath, spec); err != nil {
		return nil, err
	}
	exp.BlockCount = len(spec.Blocks)

	if injection == "truncate" {
		doc, err := wav.Read(opts.OutputPath, wav.DefaultOptions())
		if err != nil {
			return nil, err
		}
		last := doc.Blocks[len(doc.Blocks)-1]
		if err := os.Truncate(opts.OutputPath, doc.SourceSize-TruncateTailBytes); err != nil {
			return nil, media.NewError(media.CodeInternal, media.StageParse, last.DataOffset, "truncate fixture", err)
		}
		frameBytes := format.FrameByteCount()
		remaining := DemoBlockFrames*frameBytes - TruncateTailBytes
		complete := int64(remaining / frameBytes)
		exp.CorruptOffset = last.DataOffset + complete*int64(frameBytes)
	}

	if err := os.MkdirAll(filepath.Dir(opts.OutputPath), 0o755); err != nil {
		return nil, err
	}
	return &Generated{Path: opts.OutputPath, Expected: exp}, nil
}

func makeBlockPCM(seed int64, block int, rng *rand.Rand) []byte {
	const frames = DemoBlockFrames
	pcm := make([]byte, frames*DemoChannels*2)
	for f := 0; f < frames; f++ {
		global := block*frames + f
		wave := 11000 * math.Sin(2*math.Pi*float64(global)/64.0)
		noise := float64(rng.Intn(1201) - 600)
		v := int16(wave + noise)
		for ch := 0; ch < DemoChannels; ch++ {
			sample := v
			if global == 17 && ch == 0 {
				sample = 32767
			}
			if global == 29 && ch == 1 {
				sample = -32768
			}
			off := (f*DemoChannels + ch) * 2
			binary.LittleEndian.PutUint16(pcm[off:off+2], uint16(sample))
		}
	}
	return pcm
}

func insertBlock(blocks []BlockSpec, at int, b BlockSpec) []BlockSpec {
	out := make([]BlockSpec, 0, len(blocks)+1)
	out = append(out, blocks[:at]...)
	out = append(out, b)
	out = append(out, blocks[at:]...)
	return out
}
