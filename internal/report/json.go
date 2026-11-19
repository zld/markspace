// Package report renders pipeline results as stable JSON and as a
// key=value terminal summary that build scripts can parse with awk or grep.
//
// The JSON shape is part of the CLI contract. Struct fields are emitted in
// declaration order by encoding/json, slices are sorted before rendering, and
// no maps are used, so identical results always serialize identically.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"mediakey/internal/reason"
)

// Status values used in per-segment and license results.
const (
	StatusOK     = "ok"
	StatusFailed = "failed"
)

// SegmentResult is the outcome for one segment.
type SegmentResult struct {
	SegmentIndex int         `json:"segment_index"`
	KeyID        string      `json:"key_id"`
	Status       string      `json:"status"`
	Reason       reason.Code `json:"reason"`
	Detail       string      `json:"detail,omitempty"`
}

// Rotation records one key rotation and the first segment that used the new
// key.
type Rotation struct {
	Rotation  int    `json:"rotation"`
	FromKeyID string `json:"from_key_id"`
	ToKeyID   string `json:"to_key_id"`
	AtSegment int    `json:"at_segment"`
}

// LicenseResult describes license admission, which happens before any
// segment is decrypted.
type LicenseResult struct {
	Serial    string      `json:"serial"`
	KeyMin    int         `json:"key_min"`
	KeyMax    int         `json:"key_max"`
	IssuedAt  string      `json:"issued_at"`
	ExpiresAt string      `json:"expires_at"`
	Status    string      `json:"status"`
	Reason    reason.Code `json:"reason"`
	Detail    string      `json:"detail,omitempty"`
}

// ReplayResult describes a concurrent re-presentation of the same license
// serial. A healthy run has exactly one success and the rest rejected as
// LICENSE_REPLAY.
type ReplayResult struct {
	Checked       bool        `json:"checked"`
	Serial        string      `json:"serial"`
	Attempts      int         `json:"attempts"`
	Successes     int         `json:"successes"`
	Replays       int         `json:"replays"`
	SuccessReason reason.Code `json:"success_reason"`
	ReplayReason  reason.Code `json:"replay_reason"`
	Rejected      bool        `json:"rejected"`
}

// ManifestCheck summarizes the structural comparison of the manifest with
// the segments actually delivered. Content authentication is reported per
// segment instead.
type ManifestCheck struct {
	Checked  bool        `json:"checked"`
	Declared int         `json:"declared_segment_count"`
	Present  int         `json:"present_segment_count"`
	Status   string      `json:"status"`
	Reason   reason.Code `json:"reason"`
	Detail   string      `json:"detail,omitempty"`
}

// BoundaryCheck describes a second validation after the injected clock has
// been advanced, used to demonstrate license and key expiry. It is a separate
// observation and does not change the main verdict.
type BoundaryCheck struct {
	Checked    bool   `json:"checked"`
	AdvancedBy string `json:"advanced_by"`
	// LicenseReason is the license admission result at the advanced time.
	LicenseReason reason.Code `json:"license_reason"`
	LicenseDetail string      `json:"license_detail,omitempty"`
	// KeyProbeOK/Failed and Segments come from a probe with a freshly issued
	// license at the advanced time. That isolates expired key versions from an
	// expired license, so each segment shows KEY_EXPIRED rather than the
	// license-level failure.
	KeyProbeOK     int             `json:"key_probe_ok_count"`
	KeyProbeFailed int             `json:"key_probe_failed_count"`
	Segments       []SegmentResult `json:"segments"`
}

// Report is the complete result of one demo or validation run.
type Report struct {
	AssetID       string          `json:"asset_id"`
	Verdict       string          `json:"verdict"`
	RotationCount int             `json:"rotation_count"`
	SegmentCount  int             `json:"segment_count"`
	OKCount       int             `json:"ok_count"`
	FailedCount   int             `json:"failed_count"`
	License       LicenseResult   `json:"license"`
	Manifest      ManifestCheck   `json:"manifest_check"`
	Rotations     []Rotation      `json:"rotations"`
	Segments      []SegmentResult `json:"segments"`
	Replay        ReplayResult    `json:"replay"`
	Boundary      BoundaryCheck   `json:"boundary_check"`
}

// ErrorReport is emitted when a run cannot start, for example on a bad
// command-line argument or a packaging failure.
type ErrorReport struct {
	Reason  reason.Code `json:"reason"`
	Message string      `json:"message"`
}

// Finalize sorts the slices, recomputes counts and derives the verdict. It is
// idempotent.
func (r *Report) Finalize() {
	sort.SliceStable(r.Segments, func(i, j int) bool {
		return r.Segments[i].SegmentIndex < r.Segments[j].SegmentIndex
	})
	sort.SliceStable(r.Rotations, func(i, j int) bool {
		return r.Rotations[i].Rotation < r.Rotations[j].Rotation
	})
	sort.SliceStable(r.Boundary.Segments, func(i, j int) bool {
		return r.Boundary.Segments[i].SegmentIndex < r.Boundary.Segments[j].SegmentIndex
	})
	r.SegmentCount = len(r.Segments)
	r.OKCount = 0
	r.FailedCount = 0
	for _, s := range r.Segments {
		if s.Status == StatusOK && s.Reason == reason.NONE {
			r.OKCount++
		} else {
			r.FailedCount++
		}
	}
	r.Verdict = StatusOK
	if r.FailedCount > 0 || r.License.Status == StatusFailed ||
		(r.Manifest.Checked && r.Manifest.Status == StatusFailed) {
		r.Verdict = StatusFailed
	}
}

// ExitCode maps the verdict to the CLI exit code: 0 for a clean validation,
// 2 when any check failed.
func (r *Report) ExitCode() int {
	if r.Verdict == StatusOK && r.FailedCount == 0 {
		return 0
	}
	return 2
}

// EncodeJSON renders the report as indented JSON with a trailing newline.
func (r *Report) EncodeJSON() ([]byte, error) {
	r.Finalize()
	buf, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("report: encode: %w", err)
	}
	return append(buf, '\n'), nil
}

// WriteJSON writes the JSON rendering of r to w.
func (r *Report) WriteJSON(w io.Writer) error {
	buf, err := r.EncodeJSON()
	if err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}

// WriteErrorJSON writes a failure envelope for a run that could not start.
func WriteErrorJSON(w io.Writer, code reason.Code, message string) error {
	buf, err := json.MarshalIndent(ErrorReport{Reason: code, Message: message}, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = w.Write(buf)
	return err
}

// WriteSummary writes one line per fact. Every line is a sequence of
// key=value pairs separated by single spaces so shell scripts can parse it
// without a JSON decoder.
func (r *Report) WriteSummary(w io.Writer) error {
	r.Finalize()
	if _, err := fmt.Fprintf(w, "summary asset_id=%s verdict=%s rotation_count=%d segment_count=%d ok_count=%d failed_count=%d\n",
		r.AssetID, r.Verdict, r.RotationCount, r.SegmentCount, r.OKCount, r.FailedCount); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "license serial=%s status=%s reason=%s key_range=%d-%d\n",
		r.License.Serial, r.License.Status, r.License.Reason, r.License.KeyMin, r.License.KeyMax); err != nil {
		return err
	}
	for _, rot := range r.Rotations {
		if _, err := fmt.Fprintf(w, "rotation index=%d from=%s to=%s at_segment=%d\n",
			rot.Rotation, rot.FromKeyID, rot.ToKeyID, rot.AtSegment); err != nil {
			return err
		}
	}
	for _, s := range r.Segments {
		if _, err := fmt.Fprintf(w, "segment segment_index=%d key_id=%s status=%s reason=%s",
			s.SegmentIndex, s.KeyID, s.Status, s.Reason); err != nil {
			return err
		}
		if s.Detail != "" {
			if _, err := fmt.Fprintf(w, " detail=%q", s.Detail); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	if r.Replay.Checked {
		if _, err := fmt.Fprintf(w, "replay serial=%s attempts=%d successes=%d replays=%d success_reason=%s replay_reason=%s rejected=%t\n",
			r.Replay.Serial, r.Replay.Attempts, r.Replay.Successes, r.Replay.Replays,
			r.Replay.SuccessReason, r.Replay.ReplayReason, r.Replay.Rejected); err != nil {
			return err
		}
	}
	if r.Boundary.Checked {
		if _, err := fmt.Fprintf(w, "boundary advanced_by=%s license_reason=%s key_probe_ok_count=%d key_probe_failed_count=%d\n",
			r.Boundary.AdvancedBy, r.Boundary.LicenseReason,
			r.Boundary.KeyProbeOK, r.Boundary.KeyProbeFailed); err != nil {
			return err
		}
		for _, s := range r.Boundary.Segments {
			if _, err := fmt.Fprintf(w, "boundary_segment segment_index=%d key_id=%s status=%s reason=%s",
				s.SegmentIndex, s.KeyID, s.Status, s.Reason); err != nil {
				return err
			}
			if s.Detail != "" {
				if _, err := fmt.Fprintf(w, " detail=%q", s.Detail); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
	}
	return nil
}
