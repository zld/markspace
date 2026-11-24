// Package manifest emits the local processing manifest. The document itself
// is JSON for easy inspection and is encapsulated inside an ISO/IEC 14496-12
// style container via github.com/abema/go-mp4: an ftyp box marks the local
// brand and a custom "mdmd" box carries the JSON payload. Every output slice,
// fingerprint and error is traceable to its inputs and checkpoint.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
	"mediad/pipeline"
)

// Brand is the local major brand written into the ftyp box.
const Brand = "mdmd"

// BoxTypeManifest is the four-character type of the JSON-carrying box.
var BoxTypeManifest = mp4.BoxType{'m', 'd', 'm', 'd'}

// SliceRecord is one output slice with its measured time and bitrate.
type SliceRecord struct {
	TrackID          string `json:"track_id"`
	StartTicks       int64  `json:"start_ticks"`
	EndTicks         int64  `json:"end_ticks"`
	StartMs          int64  `json:"start_ms"`
	EndMs            int64  `json:"end_ms"`
	Bytes            int64  `json:"bytes"`
	TargetBitrateBPS int64  `json:"bitrate_target_bps"`
	ActualBitrateBPS int64  `json:"bitrate_actual_bps"`
}

// FingerprintRecord traces one content window to its hash.
type FingerprintRecord struct {
	TrackID     string `json:"track_id"`
	WindowIndex int    `json:"window_index"`
	ContentHash string `json:"content_hash"`
	Reused      bool   `json:"reused"`
}

// SilenceRecord is one detected silence interval.
type SilenceRecord struct {
	TrackID string `json:"track_id"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
}

// Manifest is the complete, content-addressed output document.
type Manifest struct {
	SchemaVersion string                `json:"schema_version"`
	Inputs        []InputRecord         `json:"inputs"`
	Slices        []SliceRecord         `json:"slices"`
	Fingerprints  []FingerprintRecord   `json:"fingerprints"`
	Silences      []SilenceRecord       `json:"silences"`
	Errors        []clock.RangeError    `json:"errors"`
	Checkpoints   []pipeline.Checkpoint `json:"checkpoints"`
	// Stats carries the acceptance metrics printed by the CLI.
	Stats Stats `json:"stats"`
}

// InputRecord names one source and its size.
type InputRecord struct {
	Name        string `json:"name"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentHash string `json:"content_hash"`
}

// Stats are the headline acceptance numbers.
type Stats struct {
	BitrateTargetBPS int64 `json:"bitrate_target_bps"`
	BitrateActualBPS int64 `json:"bitrate_actual_bps"`
	QualityErrors    int   `json:"quality_errors"`
	ManifestMaxDelta int64 `json:"manifest_max_delta_ms"`
	DuplicateCount   int   `json:"duplicate_count"`
	CheckpointReused int   `json:"checkpoint_reused"`
}

// New returns an empty manifest pinned to the current schema version.
func New() *Manifest {
	return &Manifest{SchemaVersion: pipeline.CodeVersion}
}

// DocumentHash is the content hash of the canonical JSON, used to prove that
// an interrupted-then-resumed run produced an identical manifest.
func (m *Manifest) DocumentHash() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("manifest: cannot hash document: %w", err)
	}
	return sha256Hex(b), nil
}

// MarshalJSON keeps the document canonical (sorted keys via encoding/json).
func (m *Manifest) jsonBytes() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// Write encapsulates the JSON manifest in an MP4-style container.
func (m *Manifest) Write(w io.WriteSeeker) error {
	payload, err := m.jsonBytes()
	if err != nil {
		return err
	}
	writer := mp4.NewWriter(w)
	// ftyp: local brand, no minor version or compatible brands.
	if _, err := writer.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := writer.Write(ftypPayload()); err != nil {
		return err
	}
	if _, err := writer.EndBox(); err != nil {
		return err
	}
	// mdmd: custom box whose payload is the JSON document.
	if _, err := writer.StartBox(&mp4.BoxInfo{Type: BoxTypeManifest}); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	if _, err := writer.EndBox(); err != nil {
		return err
	}
	return nil
}

// Read parses a manifest back out of its container, validating that the ftyp
// brand and the mdmd box are both present.
func Read(r io.ReadSeeker) (*Manifest, error) {
	var sawBrand, sawManifest bool
	var payload []byte
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type.String() {
		case "ftyp":
			sawBrand = true
			return h.Expand()
		case "mdmd":
			sawManifest = true
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, err
			}
			payload = buf.Bytes()
			return nil, nil
		}
		return h.Expand()
	})
	if err != nil {
		return nil, fmt.Errorf("manifest: cannot read container: %w", err)
	}
	if !sawBrand || !sawManifest {
		return nil, fmt.Errorf("manifest: container missing ftyp or mdmd box")
	}
	var m Manifest
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, fmt.Errorf("manifest: corrupt JSON payload: %w", err)
	}
	return &m, nil
}

// RoundTrip encodes to bytes and decodes again, used by tests and verification.
func RoundTrip(m *Manifest) (*Manifest, error) {
	var buf bytes.Buffer
	seeker := newBufferSeeker(&buf)
	if err := m.Write(seeker); err != nil {
		return nil, err
	}
	return Read(bytes.NewReader(buf.Bytes()))
}
