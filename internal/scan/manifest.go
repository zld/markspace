package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"mediacore/internal/clock"
)

// ManifestVersion is bumped whenever the sidecar schema changes.
const ManifestVersion uint32 = 1

// Manifest is the JSON sidecar delivered alongside an MP4 asset. It carries
// information the MP4 container cannot honestly encode for a synthetic
// long-duration fixture:
//
//   - WireSizes: per-sample logical network payload sizes in bytes. These
//     are the values bitrate/quality statistics are computed from; physical
//     mdat payloads may be compact placeholders.
//   - StartNanos/EndNanos: the manifest's declared window on the unified
//     timeline, including non-zero origins (edit-list remapping).
//   - ForceNonSync: sample numbers (1-based) whose sync flag must be
//     suppressed, used by the missing-keyframe recovery fixture.
type Manifest struct {
	ManifestVersion uint32 `json:"manifest_version"`
	TrackID         uint32 `json:"track_id"`
	Timescale       uint32 `json:"timescale"`
	SampleCount     uint32 `json:"sample_count"`
	StartNanos      int64  `json:"start_nanos"`
	EndNanos        int64  `json:"end_nanos"`
	// MediaStartTicks is the non-zero first-sample decode time (edit-list
	// media_time origin). Zero means samples begin at timeline zero.
	MediaStartTicks int64    `json:"media_start_ticks,omitempty"`
	WireSizes       []uint32 `json:"wire_sizes"`
	ForceNonSync    []uint32 `json:"force_non_sync,omitempty"`
	JitterSeed      uint64   `json:"jitter_seed"`
	// DigestHex is filled in by LoadManifest after reading the MP4 bytes.
	DigestHex string `json:"input_digest_hex,omitempty"`
}

// LoadManifest reads and validates a sidecar. The MP4 digest is computed by
// streaming the asset so fixtures of any size cost constant memory.
func LoadManifest(mp4Path, manifestPath string) (*Manifest, string, error) {
	f, err := os.Open(manifestPath)
	if err != nil {
		return nil, "", clock.ErrRecoverablef("scan.manifest", "open sidecar: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", clock.ErrRecoverablef("scan.manifest", "read sidecar: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, "", clock.ErrRecoverablef("scan.manifest", "invalid sidecar json: %v", err)
	}
	if m.ManifestVersion != ManifestVersion {
		return nil, "", clock.ErrRecoverablef("scan.manifest", "sidecar version %d unsupported (want %d)", m.ManifestVersion, ManifestVersion)
	}
	if m.Timescale == 0 || m.SampleCount == 0 {
		return nil, "", clock.ErrInvalidArgument("manifest: timescale and sample_count must be non-zero")
	}
	if len(m.WireSizes) != int(m.SampleCount) {
		return nil, "", clock.ErrRecoverablef("scan.manifest", "wire_sizes len %d != sample_count %d", len(m.WireSizes), m.SampleCount)
	}
	if m.EndNanos <= m.StartNanos {
		return nil, "", clock.ErrInvalidArgument("manifest: end_nanos must exceed start_nanos")
	}
	digest, err := DigestFile(mp4Path)
	if err != nil {
		return nil, "", err
	}
	m.DigestHex = digest
	return &m, digest, nil
}

// DigestFile returns the hex SHA-256 of a file, streamed in 1 MiB blocks.
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", clock.ErrRecoverablef("scan.digest", "open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", clock.ErrRecoverablef("scan.digest", "hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TotalWireBytes sums logical payload sizes with an overflow guard.
func (m *Manifest) TotalWireBytes() (uint64, error) {
	var total uint64
	for i, sz := range m.WireSizes {
		if total+uint64(sz) < total {
			return 0, clock.WithTrack(clock.ErrOverflow("scan.manifest"), m.TrackID, int64(i))
		}
		total += uint64(sz)
	}
	return total, nil
}

// String renders a compact deterministic summary for reports.
func (m *Manifest) String() string {
	return fmt.Sprintf("track=%d samples=%d timescale=%d window=[%d,%d]ns",
		m.TrackID, m.SampleCount, m.Timescale, m.StartNanos, m.EndNanos)
}
