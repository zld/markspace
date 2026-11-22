package ebml

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"mediaqc/internal/timeline"
)

// IndexVersion is bumped whenever the on-disk index layout changes;
// stale indexes are discarded and the input is rescanned.
const IndexVersion = 1

// IndexEntry is one seekable block position. Keyframe entries form the
// random-access index; non-key entries let the runner stream in order.
type IndexEntry struct {
	Offset      int64  `json:"offset"`
	Size        int64  `json:"size"`
	Track       uint64 `json:"track"`
	RawTimecode int64  `json:"raw_timecode"`
	Keyframe    bool   `json:"keyframe"`
	PayloadOff  int64  `json:"payload_off"`
	PayloadLen  int64  `json:"payload_len"`
}

// Index binds a scan result to the SHA-256 of the exact input bytes it
// was built from. The Checksum covers every field above it, so any
// tampering or version drift is detected before use.
type Index struct {
	Version        int          `json:"version"`
	InputSHA256    string       `json:"input_sha256"`
	TimestampScale uint64       `json:"timestamp_scale"`
	Tracks         []TrackInfo  `json:"tracks"`
	Entries        []IndexEntry `json:"entries"`
	Checksum       string       `json:"checksum"`
}

// Build constructs an index from a scan result and the input digest.
func Build(res *ScanResult, inputSHA [32]byte) *Index {
	idx := &Index{
		Version:        IndexVersion,
		InputSHA256:    hex.EncodeToString(inputSHA[:]),
		TimestampScale: res.TimestampScale,
		Tracks:         res.Tracks,
	}
	for _, b := range res.Blocks {
		idx.Entries = append(idx.Entries, IndexEntry{
			Offset:      b.Offset,
			Size:        b.Size,
			Track:       b.Track,
			RawTimecode: b.RawTimecode,
			Keyframe:    b.Keyframe,
			PayloadOff:  b.PayloadOff,
			PayloadLen:  b.PayloadLen,
		})
	}
	idx.Checksum = idx.computeChecksum()
	return idx
}

// computeChecksum hashes the canonical JSON of every field except the
// checksum itself.
func (idx *Index) computeChecksum() string {
	body := struct {
		Version        int          `json:"version"`
		InputSHA256    string       `json:"input_sha256"`
		TimestampScale uint64       `json:"timestamp_scale"`
		Tracks         []TrackInfo  `json:"tracks"`
		Entries        []IndexEntry `json:"entries"`
	}{idx.Version, idx.InputSHA256, idx.TimestampScale, idx.Tracks, idx.Entries}
	data, _ := json.Marshal(body)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifyChecksum recomputes the checksum and compares it.
func (idx *Index) VerifyChecksum() bool {
	return idx.Checksum == idx.computeChecksum()
}

// Keyframes returns the entries usable as random-access points.
func (idx *Index) Keyframes() []IndexEntry {
	var out []IndexEntry
	for _, e := range idx.Entries {
		if e.Keyframe {
			out = append(out, e)
		}
	}
	return out
}

// SaveIndex writes the index atomically (temp file + rename).
func SaveIndex(path string, idx *Index) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadIndex reads an index and binds it to the input digest. Any
// version, digest or checksum mismatch is a ClassIndexMismatch fault:
// the caller must discard the index and rescan from scratch.
func LoadIndex(path string, inputSHA [32]byte) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, timeline.NewFault(timeline.ClassIndexMismatch, 0, 0,
			"index at %s is not parseable: %v", path, err)
	}
	wantSHA := hex.EncodeToString(inputSHA[:])
	if idx.Version != IndexVersion {
		return nil, timeline.NewFault(timeline.ClassIndexMismatch, 0, 0,
			"index version %d, want %d", idx.Version, IndexVersion)
	}
	if idx.InputSHA256 != wantSHA {
		return nil, timeline.NewFault(timeline.ClassIndexMismatch, 0, 0,
			"index bound to input %s, have %s", idx.InputSHA256, wantSHA)
	}
	if !idx.VerifyChecksum() {
		return nil, timeline.NewFault(timeline.ClassIndexMismatch, 0, 0,
			"index checksum mismatch")
	}
	return &idx, nil
}

// SidecarPath returns the conventional index path for an input file.
func SidecarPath(inputPath string) string {
	return fmt.Sprintf("%s.qcindex", inputPath)
}
