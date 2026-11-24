// Package output builds the machine-readable summaries the CLI prints and
// verifies delivered results. Every digest is SHA-256 over a canonical,
// newline-delimited encoding so identical runs produce identical sums.
package output

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"mediasync/internal/model"
)

// Exit codes shared by the CLI and the verification helpers.
const (
	ExitOK        = 0
	ExitFailure   = 1
	ExitCorrupted = 2
)

// Record is one released media event in canonical output order.
type Record struct {
	TrackID   uint32          `json:"track"`
	Kind      model.TrackKind `json:"kind"`
	ExtSeq    int64           `json:"ext_seq"`
	Keyframe  bool            `json:"keyframe"`
	Original  model.Nanos     `json:"original_ns"`
	Corrected model.Nanos     `json:"corrected_ns"`
	Size      int             `json:"size"`
}

// Digest returns the SHA-256 of a record stream in the given order. The
// encoding is stable: fixed field order, integer nanoseconds, no floats.
func Digest(recs []Record) string {
	h := sha256.New()
	for _, r := range recs {
		fmt.Fprintf(h, "%d|%s|%d|%t|%d|%d|%d\n",
			r.TrackID, r.Kind, r.ExtSeq, r.Keyframe, r.Original, r.Corrected, r.Size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Summary is the machine-readable run report. Fields are always emitted, so
// downstream tooling can rely on their presence.
type Summary struct {
	Command         string   `json:"command"`
	OK              bool     `json:"ok"`
	ExitCode        int      `json:"exit_code"`
	Seed            int64    `json:"seed"`
	WrapBits        uint     `json:"wrap_bits"`
	PacketTotal     int64    `json:"packet_total"`
	PacketsDup      int64    `json:"packets_duplicate"`
	Lost            int64    `json:"lost"`
	Recovered       int64    `json:"recovered"`
	Unrecoverable   int64    `json:"unrecoverable"`
	LateRecomputed  int64    `json:"late_recomputed"`
	LateEvents      int64    `json:"late_events"`
	MaxAVSkewMS     float64  `json:"max_av_skew_ms"`
	DriftPPM        float64  `json:"drift_ppm_estimate"`
	DriftClamped    bool     `json:"drift_clamped"`
	Checkpoints     int      `json:"checkpoints"`
	ResumedFrom     int64    `json:"resumed_from"`
	ReplayEvents    int64    `json:"replay_events"`
	ResumeMS        float64  `json:"resume_elapsed_ms"`
	ErrorCount      int      `json:"error_count"`
	LocatedOffset   int64    `json:"located_offset"`
	ExpectedOffset  int64    `json:"expected_offset"`
	OffsetDeviation int64    `json:"offset_deviation"`
	Fixture         string   `json:"fixture"`
	FallbackReason  string   `json:"fallback_reason,omitempty"`
	Digest          string   `json:"sha256"`
	Messages        []string `json:"messages,omitempty"`
}

// WriteJSON writes the summary as indented JSON with a trailing newline.
func WriteJSON(w io.Writer, s Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// VerifyTruncation compares the located damage offset with the expected
// injection point. It returns the exit code the CLI must use: 0 when within
// one byte, 2 when the input is damaged but the position was located, and 1
// when the damage was not located at all.
func VerifyTruncation(located, expected int64, errors int) (int, int64) {
	dev := located - expected
	if dev < 0 {
		dev = -dev
	}
	if errors == 0 {
		return ExitFailure, dev
	}
	if dev <= 1 {
		return ExitCorrupted, dev
	}
	return ExitFailure, dev
}

// CheckpointVerify verifies that a resumed run's digest matches the reference
// digest and reports the outcome as a summary field set.
func CheckpointVerify(resumed, reference string) (bool, string) {
	if resumed == "" || reference == "" {
		return false, "missing digest"
	}
	if resumed != reference {
		return false, fmt.Sprintf("digest mismatch: resumed=%s reference=%s", resumed, reference)
	}
	return true, ""
}
