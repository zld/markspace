// Package fixture generates the complete offline sample corpus: HLS and
// DASH playlists, fMP4 (styp+moof+mdat) segment files and RTP capture
// files for the valid, burst-loss, repaired, truncated and
// duplicate-order scenarios. Nothing is downloaded and no external file
// is referenced; every byte is synthesized deterministically.
package fixture

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

// Scenario identifiers accepted on the command line.
const (
	ScenarioValid          = "valid"
	ScenarioBurstLoss      = "burst-loss"
	ScenarioRepaired       = "repaired"
	ScenarioTruncated      = "truncated"
	ScenarioDuplicateOrder = "duplicate-order"
)

// Scenarios lists every supported fixture name.
var Scenarios = []string{
	ScenarioValid, ScenarioBurstLoss, ScenarioRepaired,
	ScenarioTruncated, ScenarioDuplicateOrder,
}

const (
	// FramesPerSegment and SegmentSeconds fix the fixture subset at a
	// 30 fps, one-second segment grid.
	FramesPerSegment = 30
	SegmentSeconds   = 1
	// SegmentCount is the number of segments per track.
	SegmentCount = 3
	// MTU caps one RTP payload, matching a small embedded link.
	MTU = 128
)

// Config parameterizes generation.
type Config struct {
	Scenario      string
	Tracks        int
	BandwidthKbps []int
	Window        int
}

// Fixture describes a generated corpus.
type Fixture struct {
	Root     string
	Scenario string
	Window   int
	Tracks   []media.TrackSpec
	// TrackDir maps track ID to its directory.
	TrackDir map[uint32]string
}

// ArchMagic prefixes every synthetic frame payload.
var ArchMagic = [4]byte{'A', 'R', 'C', 'H'}

const (
	archHeaderLen = 19
	archVersion   = 1
	flagCorrupt   = 0x01
)

// FrameSpec is one frame's deterministic content description.
type FrameSpec struct {
	TrackID    uint32
	SegmentSeq uint32
	Index      uint32
	Width      uint16
	Height     uint16
	Luma       uint8
	Size       int // total frame size including the ARCH header
}

// BuildFrame renders the deterministic synthetic frame byte string.
// Identical specs always produce identical bytes, which is what makes
// repeated runs hash-stable.
func BuildFrame(f FrameSpec) []byte {
	buf := make([]byte, f.Size)
	copy(buf[0:4], ArchMagic[:])
	buf[4] = archVersion
	buf[5] = 0
	binary.BigEndian.PutUint32(buf[6:10], f.TrackID)
	binary.BigEndian.PutUint16(buf[10:12], uint16(f.SegmentSeq))
	binary.BigEndian.PutUint16(buf[12:14], uint16(f.Index))
	binary.BigEndian.PutUint16(buf[14:16], f.Width)
	binary.BigEndian.PutUint16(buf[16:18], f.Height)
	buf[18] = f.Luma
	// Deterministic synthetic pixel fill, derived only from frame ids.
	for i := archHeaderLen; i < f.Size; i++ {
		buf[i] = byte((int(f.TrackID)*7 + int(f.SegmentSeq)*13 +
			int(f.Index)*17 + i*31) & 0xff)
	}
	return buf
}

// ParseArchFrame validates and decodes one ARCH frame. A short buffer or
// a bad magic/header is reported as a decode error rather than guessed.
func ParseArchFrame(buf []byte) (FrameSpec, error) {
	if len(buf) < archHeaderLen {
		return FrameSpec{}, merr.New(merr.KindDecode, "fixture", "arch",
			"frame payload shorter than ARCH header")
	}
	var magic [4]byte
	copy(magic[:], buf[0:4])
	if magic != ArchMagic {
		return FrameSpec{}, merr.New(merr.KindContainer, "fixture", "arch",
			"unsupported frame format, expected ARCH magic")
	}
	if buf[4] != archVersion {
		return FrameSpec{}, merr.New(merr.KindContainer, "fixture", "arch",
			"unsupported ARCH frame version")
	}
	return FrameSpec{
		TrackID:    binary.BigEndian.Uint32(buf[6:10]),
		SegmentSeq: uint32(binary.BigEndian.Uint16(buf[10:12])),
		Index:      uint32(binary.BigEndian.Uint16(buf[12:14])),
		Width:      binary.BigEndian.Uint16(buf[14:16]),
		Height:     binary.BigEndian.Uint16(buf[16:18]),
		Luma:       buf[18],
		Size:       len(buf),
	}, nil
}

// Generate writes the full corpus for cfg into root and returns its
// description. The operation is deterministic end to end.
func Generate(cfg Config, root string) (*Fixture, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "hls"), 0o755); err != nil {
		return nil, merr.Wrap(err, "fixture", root, "cannot create fixture root")
	}
	if err := os.MkdirAll(filepath.Join(root, "dash"), 0o755); err != nil {
		return nil, err
	}
	fx := &Fixture{
		Root: root, Scenario: cfg.Scenario, Window: cfg.Window,
		TrackDir: map[uint32]string{},
	}
	for t := 0; t < cfg.Tracks; t++ {
		spec := trackSpec(t, cfg.BandwidthKbps[t])
		fx.Tracks = append(fx.Tracks, spec)
		dir := filepath.Join(root, trackDirName(spec.ID))
		fx.TrackDir[spec.ID] = dir
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, merr.Wrap(err, "fixture", dir, "cannot create track dir")
		}
		for seq := uint32(1); seq <= SegmentCount; seq++ {
			frames := buildFrameSpecs(spec, seq)
			if err := writeSegment(fx, spec, seq, frames, cfg); err != nil {
				return nil, err
			}
			if err := writeCapture(fx, spec, seq, frames, cfg); err != nil {
				return nil, err
			}
		}
	}
	if err := WriteHLSPlaylist(fx, filepath.Join(root, "hls", "index.m3u8")); err != nil {
		return nil, err
	}
	if err := WriteDASHPlaylist(fx, filepath.Join(root, "dash", "manifest.mpd")); err != nil {
		return nil, err
	}
	if err := writeMeta(fx); err != nil {
		return nil, err
	}
	return fx, nil
}

// ValidateConfig checks generation parameters.
func ValidateConfig(cfg Config) error {
	if !knownScenario(cfg.Scenario) {
		return merr.New(merr.KindArgument, "fixture", "scenario",
			"unknown fixture scenario: "+cfg.Scenario)
	}
	if cfg.Tracks < 1 {
		return merr.New(merr.KindArgument, "fixture", "tracks",
			"track count must be >= 1")
	}
	if len(cfg.BandwidthKbps) != cfg.Tracks {
		return merr.New(merr.KindArgument, "fixture", "bandwidth-kbps",
			"one bandwidth value per track is required")
	}
	for _, b := range cfg.BandwidthKbps {
		if b <= 0 {
			return merr.New(merr.KindArgument, "fixture", "bandwidth-kbps",
				"bandwidth must be positive")
		}
	}
	if cfg.Window < 1 {
		return merr.New(merr.KindArgument, "fixture", "window",
			"reorder window must be >= 1")
	}
	return nil
}

func knownScenario(s string) bool {
	for _, k := range Scenarios {
		if k == s {
			return true
		}
	}
	return false
}

func trackSpec(index int, kbps int) media.TrackSpec {
	widths := []uint16{160, 128, 96, 80, 64}
	heights := []uint16{90, 72, 54, 45, 36}
	id := uint32(index + 1)
	return media.TrackSpec{
		ID: id, Name: trackName(id), Kind: media.TrackVideo,
		Width: widths[index%len(widths)], Height: heights[index%len(heights)],
		TimebaseNum: FramesPerSegment, TimebaseDen: SegmentSeconds,
		TargetBitrateKbps: kbps, BandwidthKbps: kbps,
	}
}

func trackName(id uint32) string {
	return "v" + uitoa(uint64(id-1))
}

func trackDirName(id uint32) string {
	return "track" + uitoa(uint64(id))
}

// buildFrameSpecs creates one segment's deterministic frame list. A
// small deterministic per-segment permille delta (-20..+20) varies the
// delivered bytes so actual bitrate differs measurably from target.
func buildFrameSpecs(spec media.TrackSpec, seq uint32) []FrameSpec {
	base := spec.TargetBitrateKbps * 1000 / 8 / FramesPerSegment
	delta := (int(seq)*137+int(spec.ID)*31)%41 - 20
	frameSize := base * (1000 + delta) / 1000
	if frameSize < archHeaderLen+1 {
		frameSize = archHeaderLen + 1
	}
	frames := make([]FrameSpec, FramesPerSegment)
	for i := range frames {
		frames[i] = FrameSpec{
			TrackID: spec.ID, SegmentSeq: seq, Index: uint32(i),
			Width: spec.Width, Height: spec.Height,
			Luma: uint8((int(spec.ID)*11 + int(seq)*7 + i*5) & 0xff),
			Size: frameSize,
		}
	}
	return frames
}

func writeMeta(fx *Fixture) error {
	meta := map[string]interface{}{
		"scenario":           fx.Scenario,
		"window":             fx.Window,
		"frames_per_segment": FramesPerSegment,
		"segment_seconds":    SegmentSeconds,
		"segment_count":      SegmentCount,
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return merr.Wrap(err, "fixture", "fixture.json", "encode failed")
	}
	if err := os.WriteFile(filepath.Join(fx.Root, "fixture.json"), data, 0o644); err != nil {
		return merr.Wrap(err, "fixture", "fixture.json", "write failed")
	}
	return nil
}

func uitoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
