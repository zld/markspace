// Package manifest turns alignment boundaries into a deterministic, auditable
// segment manifest. Given identical input digest and configuration, repeated
// runs produce byte-identical serialised output and checksums.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/abema/go-mp4"

	"mediacheck/internal/align"
)

// Segment describes one cut for every representation.
type Segment struct {
	Index        int            `json:"index"`
	T0NS         int64          `json:"t0_ns"`
	DurationNS   int64          `json:"duration_ns"`
	RepFrameIdx  map[string]int `json:"rep_frame_index"`
}

// RepresentationSummary carries per-layer segment counts and timing span.
type RepresentationSummary struct {
	ID          string `json:"id"`
	Segments    int    `json:"segments"`
	FirstT0NS   int64  `json:"first_t0_ns"`
	LastEndNS   int64  `json:"last_end_ns"`
}

// Manifest is the complete, deterministic deliverable.
type Manifest struct {
	SchemaVersion int                   `json:"schema_version"`
	InputSHA256   string                `json:"input_sha256"`
	ConfigSHA256  string                `json:"config_sha256"`
	Segments      []Segment             `json:"segments"`
	Representations []RepresentationSummary `json:"representations"`
	OutputSHA256  string                `json:"output_sha256"`
}

// Config is the manifest-relevant subset of pipeline settings.
type Config struct {
	TargetSegmentNS int64             `json:"target_segment_ns"`
	MaxSkewNS       int64             `json:"max_skew_ns"`
	DriftLimitPPM   float64           `json:"drift_limit_ppm"`
	RepresentationIDs []string        `json:"representation_ids"`
}

// Digest hashes the canonical JSON encoding of a value.
func Digest(v any) (string, error) {
	canonical, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Builder assembles a manifest from boundaries and representation metadata.
type Builder struct {
	cfg Config
}

// NewBuilder requires a non-empty config.
func NewBuilder(cfg Config) (*Builder, error) {
	if cfg.TargetSegmentNS <= 0 {
		return nil, fmt.Errorf("manifest: target segment duration must be positive")
	}
	return &Builder{cfg: cfg}, nil
}

// Build constructs the manifest. repEndT0 gives each representation's final
// frame time so the last segment duration is bounded.
func (b *Builder) Build(inputSHA string, boundaries []align.Boundary, repEndT0 map[string]int64) (*Manifest, error) {
	if len(boundaries) < 2 {
		return nil, fmt.Errorf("manifest: need at least two boundaries, got %d", len(boundaries))
	}
	configSHA, err := Digest(b.cfg)
	if err != nil {
		return nil, err
	}
	m := &Manifest{
		SchemaVersion: 1,
		InputSHA256:   inputSHA,
		ConfigSHA256:  configSHA,
	}
	durs := align.SegmentDurations(boundaries, maxEnd(repEndT0))
	for i, bnd := range boundaries {
		m.Segments = append(m.Segments, Segment{
			Index:       bnd.Index,
			T0NS:        bnd.T0,
			DurationNS:  durs[i],
			RepFrameIdx: copyMap(bnd.FrameIndex),
		})
	}
	ids := append([]string(nil), b.cfg.RepresentationIDs...)
	sort.Strings(ids)
	for _, id := range ids {
		end, ok := repEndT0[id]
		if !ok {
			return nil, fmt.Errorf("manifest: representation %s has no end time", id)
		}
		m.Representations = append(m.Representations, RepresentationSummary{
			ID: id, Segments: len(boundaries),
			FirstT0NS: boundaries[0].T0, LastEndNS: end,
		})
	}
	sum, err := b.checksum(m)
	if err != nil {
		return nil, err
	}
	m.OutputSHA256 = sum
	return m, nil
}

func maxEnd(ends map[string]int64) int64 {
	var max int64 = -1
	for _, e := range ends {
		if e > max {
			max = e
		}
	}
	return max
}

func copyMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// checksum serialises without the checksum field and hashes the bytes.
func (b *Builder) checksum(m *Manifest) (string, error) {
	cp := *m
	cp.OutputSHA256 = ""
	raw, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Serialize returns the stable on-disk form.
func (m *Manifest) Serialize() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// ValidateStructural uses go-mp4 to prove the output-writing dependency can
// round-trip a well-formed box structure (ftyp). This guards the delivery
// toolchain without emitting any media here.
func ValidateStructural() error {
	var mem writeSeeker
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '2'}},
		},
	}
	w := mp4.NewWriter(&mem)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return fmt.Errorf("manifest: go-mp4 start box failed: %w", err)
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return fmt.Errorf("manifest: go-mp4 structural marshal failed: %w", err)
	}
	if _, err := w.EndBox(); err != nil {
		return fmt.Errorf("manifest: go-mp4 end box failed: %w", err)
	}
	raw := mem.buf
	if len(raw) < 8 || string(raw[4:8]) != "ftyp" {
		return fmt.Errorf("manifest: marshalled box is not a valid ftyp (%d bytes)", len(raw))
	}
	return nil
}

// writeSeeker is an in-memory, addressable buffer implementing io.WriteSeeker.
// Writes land at the current cursor and may rewrite earlier bytes; the
// reported length never shrinks, matching a regular file.
type writeSeeker struct {
	buf []byte
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(len(w.buf)) {
		grown := make([]byte, end)
		copy(grown, w.buf)
		w.buf = grown
	}
	copy(w.buf[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = w.pos + offset
	case io.SeekEnd:
		abs = int64(len(w.buf)) + offset
	default:
		return 0, fmt.Errorf("manifest: unsupported seek whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("manifest: negative seek position %d", abs)
	}
	w.pos = abs
	return abs, nil
}
