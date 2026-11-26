package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediarepair/internal/fsx"
	"mediarepair/internal/media"
)

const ManifestVersion = 1
const MaxDimension = 16384

type Rational = media.Rational

type ColorMetadata struct {
	Primaries string `json:"primaries"`
	Transfer  string `json:"transfer"`
	Matrix    string `json:"matrix"`
	Range     string `json:"range"`
}

type FrameRecord struct {
	Track        int            `json:"track"`
	Sequence     int            `json:"sequence"`
	RawTicks     int64          `json:"raw_ticks"`
	Timebase     media.Rational `json:"timebase"`
	Width        int            `json:"width"`
	Height       int            `json:"height"`
	Format       string         `json:"format"`
	Color        ColorMetadata  `json:"color"`
	Path         string         `json:"relative_path"`
	SHA256       string         `json:"sha256"`
	RTPTimestamp *uint32        `json:"rtp_timestamp,omitempty"`
	RTPMarker    bool           `json:"rtp_marker,omitempty"`
}

type AudioReference struct {
	Path       string `json:"relative_path"`
	SHA256     string `json:"sha256"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
}

type MP4Reference struct {
	Path   string `json:"relative_path"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version   int             `json:"version"`
	Track     int             `json:"track"`
	FrameRate media.Rational  `json:"frame_rate"`
	Timebase  media.Rational  `json:"timebase"`
	Frames    []FrameRecord   `json:"frames"`
	Audio     *AudioReference `json:"audio,omitempty"`
	MP4       *MP4Reference   `json:"mp4,omitempty"`
}

func ReadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, media.E(media.CodeManifest, "manifest.read", err).WithFile(path)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, media.E(media.CodeManifest, "manifest.parse", err).WithFile(path)
	}
	if m.Version != ManifestVersion {
		return nil, media.E(media.CodeManifest, "manifest.version", errors.New("unsupported manifest version")).WithFile(path)
	}
	if m.FrameRate.Num <= 0 || m.FrameRate.Den <= 0 {
		return nil, media.E(media.CodeManifest, "manifest.frame_rate", errors.New("frame rate must be positive")).WithFile(path)
	}
	if m.Timebase.Num <= 0 || m.Timebase.Den <= 0 {
		return nil, media.E(media.CodeManifest, "manifest.timebase", errors.New("time base must be positive")).WithFile(path)
	}
	return &m, nil
}

func SaveManifest(path string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return media.E(media.CodeManifest, "manifest.encode", err).WithFile(path)
	}
	b = append(b, '\n')
	return fsx.AtomicWriteFile(path, strings.NewReader(string(b)), 0o644)
}

func FormatForPath(path string) (string, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "png", true
	case ".jpg", ".jpeg":
		return "jpeg", true
	default:
		return "", false
	}
}

func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, media.E(media.CodeWrite, "hash.open", err).WithFile(path)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, media.E(media.CodeWrite, "hash.read", err).WithFile(path)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ValidateFrames verifies sequence uniqueness, dimensions, format, path
// containment, and SHA-256. Image headers are decoded to catch dimension lies.
func ValidateFrames(root string, m *Manifest) error {
	seen := make(map[int]int, len(m.Frames))
	for i := range m.Frames {
		fr := &m.Frames[i]
		if prev, dup := seen[fr.Sequence]; dup {
			return media.E(media.CodeDuplicateSeq, "manifest.validate", errors.New("duplicate sequence")).
				WithFile(fr.Path).WithFrame(fr.Sequence).WithOffset(int64(prev))
		}
		seen[fr.Sequence] = i

		if fr.Width <= 0 || fr.Height <= 0 || fr.Width > MaxDimension || fr.Height > MaxDimension {
			return media.E(media.CodeDimensions, "manifest.validate", errors.New("width or height outside supported range")).
				WithFile(fr.Path).WithFrame(fr.Sequence)
		}
		format, ok := FormatForPath(fr.Path)
		if !ok || (fr.Format != "" && fr.Format != format) {
			return media.E(media.CodeFormat, "manifest.validate", errors.New("only png and jpeg frame formats are supported")).
				WithFile(fr.Path).WithFrame(fr.Sequence)
		}
		full, err := fsx.SafeJoin(root, fr.Path)
		if err != nil {
			return err
		}
		gotHash, _, err := HashFile(full)
		if err != nil {
			return media.AsError(err).WithFrame(fr.Sequence)
		}
		if !strings.EqualFold(gotHash, fr.SHA256) {
			return media.E(media.CodeChecksum, "frame.sha256", errors.New("sha256 does not match manifest")).
				WithFile(fr.Path).WithFrame(fr.Sequence)
		}
		cfg, err := DecodeConfig(full)
		if err != nil {
			return media.AsError(err).WithFrame(fr.Sequence)
		}
		if cfg.Width != fr.Width || cfg.Height != fr.Height {
			return media.E(media.CodeDimensions, "frame.header", errors.New("decoded dimensions do not match manifest")).
				WithFile(fr.Path).WithFrame(fr.Sequence)
		}
	}
	return nil
}
