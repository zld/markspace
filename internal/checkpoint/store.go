package checkpoint

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"medcheck/internal/clock"
	"medcheck/internal/jitter"
	"medcheck/internal/rtp"
)

// Version is the supported checkpoint envelope version.
const Version = 1

// ClockState is the serializable subset of clock.Timeline needed for resume.
type ClockState struct {
	Have      bool   `json:"have"`
	RateHz    uint32 `json:"rate_hz"`
	FirstExt  int64  `json:"first_ext"`
	AnchorExt int64  `json:"anchor_ext"`
	AnchorNS  int64  `json:"anchor_ns"`
	MaxExt    int64  `json:"max_ext"`
}

// Sample is the serializable form of an output media sample.
type Sample struct {
	PTSNS      int64  `json:"pts_ns"`
	DurationNS int64  `json:"duration_ns"`
	Payload    []byte `json:"payload,omitempty"`
	Keyframe   bool   `json:"keyframe,omitempty"`
}

// State is a consistent packet-boundary checkpoint.
type State struct {
	Version       int                              `json:"version"`
	Boundary      int                              `json:"boundary"`
	ConfigSHA256  string                           `json:"config_sha256,omitempty"`
	InputSHA256   string                           `json:"input_sha256,omitempty"`
	NextSequences map[uint32]uint64                `json:"next_sequences,omitempty"`
	RTPMax        map[uint32]uint64                `json:"rtp_max,omitempty"`
	RTPSeen       map[uint32][]uint64              `json:"rtp_seen,omitempty"`
	JitterState   map[uint32]jitter.StreamSnapshot `json:"jitter_state,omitempty"`
	Syncer        SyncerState                      `json:"syncer,omitempty"`
	Clocks        map[string]ClockState            `json:"clocks,omitempty"`
	AwaitKeyframe map[uint32]bool                  `json:"await_keyframe,omitempty"`
	Recovered     int                              `json:"recovered"`
	IngestStats   rtp.Stats                        `json:"ingest_stats,omitempty"`
	ErrorCodes    map[string]int                   `json:"error_codes,omitempty"`
	Counts        map[string]int                   `json:"counts,omitempty"`
	AudioSamples  []Sample                         `json:"audio_samples,omitempty"`
	VideoSamples  []Sample                         `json:"video_samples,omitempty"`
}

// SyncerState is the monotonic AV-sync state needed across resume.
type SyncerState struct {
	LatestRaw    map[string]int64 `json:"latest_raw,omitempty"`
	Offsets      map[string]int64 `json:"offsets,omitempty"`
	LastAdjusted map[string]int64 `json:"last_adjusted,omitempty"`
	HaveAdjusted map[string]bool  `json:"have_adjusted,omitempty"`
	Frames       int              `json:"frames"`
	Corrections  int              `json:"corrections"`
	MaxDriftNS   int64            `json:"max_drift_ns"`
	AvDriftMS    float64          `json:"av_drift_ms"`
}

// Record is one independently checksummed JSONL checkpoint entry.
type Record struct {
	State  State  `json:"state"`
	SHA256 string `json:"sha256"`
}

// CorruptRecord describes an unusable checkpoint line.
type CorruptRecord struct {
	Line     int    `json:"line"`
	Boundary int    `json:"boundary,omitempty"`
	Reason   string `json:"reason"`
}

// LoadResult contains the nearest valid state and corruption details.
type LoadResult struct {
	State           State
	ValidBoundaries []int
	Corrupt         []CorruptRecord
}

// ErrNotFound means no checkpoint file exists.
var ErrNotFound = errors.New("checkpoint: not found")

// Append writes one checksummed boundary record as a JSON line.
func Append(path string, state State) error {
	if state.Version == 0 {
		state.Version = Version
	}
	sum, err := ChecksumState(state)
	if err != nil {
		return err
	}
	rec := Record{State: state, SHA256: sum}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// ChecksumState returns the stable SHA-256 of a checkpoint state.
func ChecksumState(state State) (string, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// LoadLatestValid scans an append-only checkpoint log and returns the newest
// record whose checksum verifies. Corrupt records are reported explicitly.
func LoadLatestValid(path string) (LoadResult, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LoadResult{}, ErrNotFound
		}
		return LoadResult{}, err
	}
	defer f.Close()

	result := LoadResult{}
	var latest *State
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			result.Corrupt = append(result.Corrupt, CorruptRecord{Line: lineNo, Reason: err.Error()})
			continue
		}
		want, err := ChecksumState(rec.State)
		if err != nil || !equalSHA(want, rec.SHA256) || rec.State.Version != Version {
			reason := "checksum mismatch"
			if rec.State.Version != Version {
				reason = fmt.Sprintf("unsupported version %d", rec.State.Version)
			}
			result.Corrupt = append(result.Corrupt, CorruptRecord{
				Line: lineNo, Boundary: rec.State.Boundary, Reason: reason,
			})
			continue
		}
		stateCopy := rec.State
		latest = &stateCopy
		result.ValidBoundaries = append(result.ValidBoundaries, rec.State.Boundary)
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if latest == nil {
		return result, clock.MediaError{
			Code:    clock.CodeCheckpointNoValidBoundary,
			Message: "checkpoint has no valid packet boundary",
		}
	}
	result.State = *latest
	if len(result.Corrupt) > 0 {
		return result, clock.MediaError{
			Code:     clock.CodeCheckpointInvalid,
			Sequence: uint64(result.Corrupt[len(result.Corrupt)-1].Boundary),
			Message: fmt.Sprintf("%d corrupt checkpoint record(s); recovered boundary %d",
				len(result.Corrupt), result.State.Boundary),
		}
	}
	return result, nil
}

// CorruptLastRecord rewrites the last non-empty line with a bad checksum.
// It is used by local corruption fixtures and tests.
func CorruptLastRecord(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) == 0 {
		return errors.New("checkpoint: empty file")
	}
	last := len(lines) - 1
	var rec Record
	if err := json.Unmarshal(lines[last], &rec); err != nil {
		// It is already corrupt; flip a byte instead.
		lines[last] = append(lines[last], ' ')
	} else {
		rec.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		lines[last] = b
	}
	var out bytes.Buffer
	for _, line := range lines {
		out.Write(line)
		out.WriteByte('\n')
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func equalSHA(a, b string) bool {
	da, err := hex.DecodeString(a)
	if err != nil {
		return false
	}
	db, err := hex.DecodeString(b)
	if err != nil || len(da) != len(db) {
		return false
	}
	return sha256.Sum256(da) == sha256.Sum256(db)
}
