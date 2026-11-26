package mp4

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"replaykit/media/fault"
)

// Sidecar is a persisted keyframe/sample index. It is only used to slice when
// its checksum verifies against the track it describes; a corrupt sidecar is
// discarded and the index is rebuilt from readable fragments.
type Sidecar struct {
	Version   int      `json:"version"`
	TrackID   uint32   `json:"track_id"`
	Timescale uint32   `json:"timescale"`
	Samples   []Sample `json:"samples"`
	Checksum  string   `json:"checksum"`
}

// SidecarVersion is the current on-disk schema.
const SidecarVersion = 1

// NewSidecar builds a sidecar from a parsed track and seals it with a checksum.
func NewSidecar(tr Track) *Sidecar {
	s := &Sidecar{Version: SidecarVersion, TrackID: tr.ID, Timescale: tr.Timescale, Samples: tr.Samples}
	s.Checksum = s.computeChecksum()
	return s
}

// computeChecksum hashes the semantic content, independent of JSON layout.
func (s *Sidecar) computeChecksum() string {
	h := sha256.New()
	fmt.Fprintf(h, "sidecar|v=%d|track=%d|ts=%d|n=%d\n", s.Version, s.TrackID, s.Timescale, len(s.Samples))
	for _, smp := range s.Samples {
		fmt.Fprintf(h, "%d,%d,%d,%d,%d,%t\n", smp.Number, smp.Offset, smp.Size, smp.DecodeTime, smp.Duration, smp.Key)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Verify checks the schema, the checksum and the track identity. A mismatch
// means the sidecar must not drive slicing.
func (s *Sidecar) Verify(tr Track) error {
	if s.Version != SidecarVersion {
		return fmt.Errorf("sidecar version %d, want %d", s.Version, SidecarVersion)
	}
	if s.TrackID != tr.ID {
		return fmt.Errorf("sidecar track %d, want %d", s.TrackID, tr.ID)
	}
	if s.Timescale != tr.Timescale {
		return fmt.Errorf("sidecar timescale %d, want %d", s.Timescale, tr.Timescale)
	}
	if got := s.computeChecksum(); got != s.Checksum {
		return fmt.Errorf("sidecar checksum mismatch: stored %s, computed %s", s.Checksum, got)
	}
	if len(s.Samples) == 0 {
		return fmt.Errorf("sidecar has no samples")
	}
	for i, smp := range s.Samples {
		if smp.Size <= 0 || smp.Duration < 0 {
			return fmt.Errorf("sidecar sample %d invalid (size=%d duration=%d)", i, smp.Size, smp.Duration)
		}
		if i > 0 && smp.DecodeTime < s.Samples[i-1].DecodeTime {
			return fmt.Errorf("sidecar sample %d out of decode order", i)
		}
	}
	return nil
}

// MarshalSidecar serialises deterministically.
func MarshalSidecar(s *Sidecar) ([]byte, error) { return json.MarshalIndent(s, "", "  ") }

// ParseSidecar reads a sidecar, malformed JSON is an error the caller reports.
func ParseSidecar(b []byte) (*Sidecar, error) {
	var s Sidecar
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("sidecar: %w", err)
	}
	return &s, nil
}

// RebuildFromFragments replaces a sidecar's samples with a freshly walked
// fragment list and reseals the checksum. Callers use it after Verify fails.
// Anomalies from discarded fragments are returned so they still reach the
// manifest.
func (s *Sidecar) RebuildFromFragments(data []byte, scan *Scan, limits Limits) ([]fault.Anomaly, error) {
	var anomalies []fault.Anomaly
	samples, err := parseFragments(data, scan, limits, &anomalies)
	if err != nil {
		return anomalies, err
	}
	if len(samples) == 0 {
		return anomalies, fmt.Errorf("no readable samples to rebuild the index from")
	}
	s.Samples = samples
	s.Checksum = s.computeChecksum()
	return anomalies, nil
}
