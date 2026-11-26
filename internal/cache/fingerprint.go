package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sort"
	"time"

	"mediarepair/internal/source"
)

func BytesSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ContentFingerprint identifies frame bytes only. Path, size, and mtime are
// deliberately not inputs.
func ContentFingerprint(frameBytes []byte) string {
	return "content:" + BytesSHA256(frameBytes)
}

// CanonicalJSONHash hashes a value through deterministic JSON encoding.
func CanonicalJSONHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return BytesSHA256(b), nil
}

// PackagingFingerprint hashes container/sample-table facts rather than only
// file size or path. The sample table includes absolute offsets but not the
// reader path when PackagingInput is used.
type PackagingInput struct {
	TrackID       uint32              `json:"track_id"`
	Timescale     uint32              `json:"timescale"`
	Duration      uint64              `json:"duration"`
	MajorBrand    string              `json:"major_brand"`
	FastStart     bool                `json:"fast_start"`
	SampleOffsets []source.SampleInfo `json:"sample_offsets"`
}

func PackagingFingerprint(st *source.SampleTable) (string, error) {
	in := PackagingInput{
		TrackID:       st.TrackID,
		Timescale:     st.Timescale,
		Duration:      st.Duration,
		MajorBrand:    st.MajorBrand,
		FastStart:     st.FastStart,
		SampleOffsets: append([]source.SampleInfo(nil), st.SampleOffsets...),
	}
	sort.Slice(in.SampleOffsets, func(i, j int) bool { return in.SampleOffsets[i].Index < in.SampleOffsets[j].Index })
	h, err := CanonicalJSONHash(in)
	if err != nil {
		return "", err
	}
	return "package:" + h, nil
}

// InputFingerprint commits to the complete manifest, including every frame
// hash, metadata value, audio reference, and package hash.
func InputFingerprint(m *source.Manifest) (string, error) {
	h, err := CanonicalJSONHash(m)
	if err != nil {
		return "", err
	}
	return "input:" + h, nil
}

// MetadataFingerprint isolates non-byte metadata so callers can invalidate
// only the random-access index.
func MetadataFingerprint(m *source.Manifest) (string, error) {
	type frameMeta struct {
		Sequence int                  `json:"sequence"`
		RawTicks int64                `json:"raw_ticks"`
		Timebase source.Rational      `json:"timebase"`
		Width    int                  `json:"width"`
		Height   int                  `json:"height"`
		Format   string               `json:"format"`
		Color    source.ColorMetadata `json:"color"`
		Path     string               `json:"path"`
	}
	meta := struct {
		Version   int             `json:"version"`
		Track     int             `json:"track"`
		FrameRate source.Rational `json:"frame_rate"`
		Timebase  source.Rational `json:"timebase"`
		Frames    []frameMeta     `json:"frames"`
	}{Version: m.Version, Track: m.Track, FrameRate: m.FrameRate, Timebase: m.Timebase}
	for _, f := range m.Frames {
		meta.Frames = append(meta.Frames, frameMeta{
			Sequence: f.Sequence, RawTicks: f.RawTicks, Timebase: f.Timebase,
			Width: f.Width, Height: f.Height, Format: f.Format, Color: f.Color, Path: f.Path,
		})
	}
	h, err := CanonicalJSONHash(meta)
	if err != nil {
		return "", err
	}
	return "metadata:" + h, nil
}

// SamePathStatKey demonstrates why stat fields alone are unsafe.
type SamePathStatKey struct {
	Path    string
	Size    int64
	ModTime time.Time
}
