package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Nominal sizes for the generated MP4 fixtures. The damage offset is chosen so
// the corrupt fixture has a valid prefix long enough to be interesting.
const (
	FixtureTimescale    = 1000
	FixtureVideoTrack   = 1
	FixtureAudioTrack   = 2
	FixtureVideoTick    = 3000 // 1/30 s at 90 kHz
	FixtureVideoSamples = 90
	FixtureAudioSamples = 200
	FixtureAudioTick    = 480
	// DamageOffset is where the corrupt fixture's moov box length is damaged.
	DamageOffset int64 = 4096
)

// Spec returns the standard generated MP4 used by verification fixtures. The
// file is sized to sit comfortably beyond DamageOffset so the truncated
// fixture points at real structure.
func Spec() MP4Spec {
	return MP4Spec{
		Timescale:   FixtureTimescale,
		AlignMdatTo: DamageOffset,
		Tracks: []MP4Track{
			{
				ID: FixtureVideoTrack, Handler: [4]byte{'v', 'i', 'd', 'e'},
				Timescale: 90_000, SampleDelta: FixtureVideoTick, SampleCount: FixtureVideoSamples,
				SyncSamples: []uint32{1, 31, 61},
				Edits:       []MP4Edit{{SegmentDuration: 3000, MediaTime: 900}},
				Width:       1920 << 16, Height: 1080 << 16,
			},
			{
				ID: FixtureAudioTrack, Handler: [4]byte{'s', 'o', 'u', 'n'},
				Timescale: 48_000, SampleDelta: FixtureAudioTick, SampleCount: FixtureAudioSamples,
				Edits: []MP4Edit{{SegmentDuration: 2000, MediaTime: 0}},
			},
		},
	}
}

// Materialize writes the named fixture into dir and returns its path and the
// injected damage offset (0 when the fixture is intact). Supported names:
//   - generated-long: a clean MP4
//   - corrupt-truncated: the same file cut short with a damaged box length
//   - edit-out-of-range: an MP4 whose edit list exceeds the media duration
func Materialize(name, dir string) (path string, injectedAt int64, err error) {
	if name == "" {
		return "", 0, fmt.Errorf("--fixture is required (generated-long, corrupt-truncated, edit-out-of-range)")
	}
	outDir := filepath.Join(dir, "fixtures")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", 0, err
	}
	clean := filepath.Join(outDir, "generated-long.mp4")
	switch name {
	case "generated-long", "generated", "clean":
		path = clean
		err = WriteFile(path, Spec())
	case "corrupt-truncated", "truncated":
		if err := WriteFile(clean, Spec()); err != nil {
			return "", 0, err
		}
		path = filepath.Join(outDir, "corrupt-truncated.mp4")
		_, cerr := CopyTruncated(clean, path, -1, DamageOffset)
		if cerr != nil {
			return "", 0, cerr
		}
		return path, DamageOffset, nil
	case "edit-out-of-range", "edit-overflow":
		path = filepath.Join(outDir, "edit-out-of-range.mp4")
		spec := Spec()
		// The first track's edit list claims more media than exists.
		spec.Tracks[0].Edits = []MP4Edit{{SegmentDuration: 4000, MediaTime: 5_000_000}}
		err = WriteFile(path, spec)
		return path, 0, err
	default:
		if _, statErr := os.Stat(name); statErr == nil {
			return name, 0, nil
		}
		return "", 0, fmt.Errorf("unknown fixture %q", name)
	}
	return path, 0, err
}

// DigestFile returns the SHA-256 of a file, used to demonstrate byte-stable
// fixture generation.
func DigestFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
