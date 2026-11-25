// Package fixture synthesizes deterministic MXF and MP4 inputs together with
// the expected metadata used by the regression pipeline.
//
// Nothing here reads external media. Every byte is generated from a seed and a
// fixed spec so that the same fixture name always produces the same input
// digest on every platform and every run.
package fixture

import (
	"fmt"
	"hash/crc32"

	"github.com/zld/media-regress/internal/error"
)

// Format identifies a supported container.
type Format string

// Supported container formats.
const (
	FormatMP4 Format = "mp4"
	FormatMXF Format = "mxf"
)

// Fixture names understood by Generate.
const (
	NameValid    = "valid"
	NameCorrupt  = "corrupt"
	NameBoundary = "boundary"
	NameGap      = "gap"
)

// Fixed profile constants shared by both container writers.
const (
	VideoTrackID = 1
	AudioTrackID = 2

	VideoTimescale  = 25
	FPS             = 25
	VideoFrames     = 50
	VideoFrameSize  = 64
	VideoWidth      = 1920
	VideoHeight     = 1080
	VideoRotation   = 90
	AudioRate       = 48000
	AudioChannels   = 2
	AudioLayout     = "stereo"
	AudioBlockFrame = 8000
	AudioBlocks     = 12
)

// AudioFrames is the total number of source audio frames.
const AudioFrames = AudioBlockFrame * AudioBlocks

// Options selects a fixture.
type Options struct {
	Name   string
	Format Format
	// CorruptBlock is a 1-based audio block number whose essence bytes are
	// damaged while its recorded checksum still describes the intact block.
	CorruptBlock int
	Seed         uint64
}

// Block describes one audio block as stored in the container.
type Block struct {
	Index      int // 1-based, matches --corrupt-block and resume_block
	TrackID    uint32
	StartFrame int64
	Frames     int64
	Offset     int64 // absolute byte offset of the essence payload
	Length     int
	CRC32      uint32
}

// TrackInfo is the expected normalized metadata for a track.
type TrackInfo struct {
	ID        uint32
	Kind      string
	Timescale uint32
	Duration  int64
	Width     uint32
	Height    uint32
	Rotation  int
	Rate      uint32
	Channels  uint16
	Layout    string
}

// Fixture is a generated input plus its expected metadata.
type Fixture struct {
	Name   string
	Format Format
	Seed   uint64
	Bytes  []byte
	Blocks []Block
	Tracks []TrackInfo
	// ExpectedConflicts counts description disagreements the normalizer must
	// report (video colour + crop in the default profile).
	ExpectedConflicts int
	// DropSegment asks the manifest builder to omit one segment, simulating a
	// publication gap. It never changes the container bytes.
	DropSegment bool
	// TruncateTimestamp marks inputs whose last sample timing entry is zeroed.
	TruncateTimestamp bool
}

// AudioTrack returns the expected audio track info.
func (f *Fixture) AudioTrack() (TrackInfo, bool) {
	for _, t := range f.Tracks {
		if t.Kind == "audio" {
			return t, true
		}
	}
	return TrackInfo{}, false
}

// Generate builds the named fixture. Unknown names are structured errors.
func Generate(opts Options) (*Fixture, error) {
	if opts.Seed == 0 {
		opts.Seed = 0x5eed5eed
	}
	if opts.Format == "" {
		opts.Format = FormatMP4
	}
	switch opts.Format {
	case FormatMP4, FormatMXF:
	default:
		return nil, merr.New(merr.StageFixture, merr.KindParse, 0, 0, "unsupported fixture format %q", opts.Format)
	}
	spec := specFor(opts)
	switch opts.Name {
	case NameValid, "":
	case NameCorrupt:
		if opts.CorruptBlock < 1 || opts.CorruptBlock > AudioBlocks {
			return nil, merr.New(merr.StageFixture, merr.KindLimit, AudioTrackID, 0,
				"corrupt block %d out of range 1..%d", opts.CorruptBlock, AudioBlocks)
		}
		spec.corruptBlock = opts.CorruptBlock
	case NameBoundary:
		if opts.Format != FormatMP4 {
			return nil, merr.New(merr.StageFixture, merr.KindParse, 0, 0,
				"boundary fixture is only defined for the mp4 profile")
		}
		spec.truncateTimestamp = true
	case NameGap:
		spec.dropSegment = true
	default:
		return nil, merr.New(merr.StageFixture, merr.KindParse, 0, 0, "unknown fixture %q", opts.Name)
	}

	var (
		bytes  []byte
		blocks []Block
		err    error
	)
	switch opts.Format {
	case FormatMP4:
		bytes, blocks, err = buildMP4(spec)
	case FormatMXF:
		bytes, blocks, err = buildMXF(spec)
	}
	if err != nil {
		return nil, err
	}
	f := &Fixture{
		Name:              opts.Name,
		Format:            opts.Format,
		Seed:              opts.Seed,
		Bytes:             bytes,
		Blocks:            blocks,
		ExpectedConflicts: 2,
		DropSegment:       spec.dropSegment,
		TruncateTimestamp: spec.truncateTimestamp,
	}
	f.Tracks = []TrackInfo{
		{
			ID: VideoTrackID, Kind: "video", Timescale: VideoTimescale,
			Duration: VideoFrames, Width: VideoWidth, Height: VideoHeight,
			Rotation: VideoRotation,
		},
		{
			ID: AudioTrackID, Kind: "audio", Timescale: AudioRate,
			Duration: AudioFrames, Rate: AudioRate, Channels: AudioChannels,
			Layout: AudioLayout,
		},
	}
	return f, nil
}

type spec struct {
	seed              uint64
	corruptBlock      int
	truncateTimestamp bool
	dropSegment       bool
	format            Format
}

func specFor(opts Options) *spec {
	return &spec{
		seed:   opts.Seed,
		format: opts.Format,
	}
}

// audioBlockBytes is the size of one interleaved s16le block.
const audioBlockBytes = AudioBlockFrame * AudioChannels * 2

// synthAudioBlock writes one deterministic interleaved block starting at
// startFrame. Integer-only synthesis keeps the bytes identical everywhere.
func synthAudioBlock(dst []byte, startFrame int64) {
	n := 0
	for f := int64(0); f < AudioBlockFrame; f++ {
		abs := startFrame + f
		for c := 0; c < AudioChannels; c++ {
			v := int16(((abs*67 + int64(c)*191) % 16384) - 8192)
			dst[n] = byte(uint16(v))
			dst[n+1] = byte(uint16(v) >> 8)
			n += 2
		}
	}
}

// synthVideoFrame writes one deterministic opaque frame.
func synthVideoFrame(dst []byte, index int) {
	for j := range dst {
		dst[j] = byte((index*31 + j*17 + 77) & 0xff)
	}
}

func validateSpec(s *spec) error {
	if s.seed == 0 {
		return fmt.Errorf("fixture: zero seed")
	}
	return nil
}

// FixtureFromBytes exists for tests that need to wrap arbitrary bytes.
func FixtureFromBytes(name string, format Format, data []byte) *Fixture {
	return &Fixture{Name: name, Format: format, Bytes: data}
}

// AudioBlockBytes is the byte length of one synthesized audio block.
const AudioBlockBytes = audioBlockBytes

// FixedCRC returns the checksum of the intact synthesized block with the given
// 1-based index. Tests use it to assert that recorded checksums disagree with
// damaged essence.
func FixedCRC(index int) uint32 {
	buf := make([]byte, audioBlockBytes)
	synthAudioBlock(buf, int64(index-1)*AudioBlockFrame)
	return crc32.ChecksumIEEE(buf)
}
