package metrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ErrInconsistent marks an internal accounting identity failure.
type consistencyError struct{ Msg string }

func (e *consistencyError) Error() string { return e.Msg }

// Inconsistent builds an internal-consistency error (exit code 4 at the CLI).
func Inconsistent(format string, args ...any) error {
	return &consistencyError{Msg: fmt.Sprintf(format, args...)}
}

// WriteJSON emits the report as indented JSON.
func WriteJSON(w io.Writer, r Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// WriteText emits a human-readable terminal summary.
func WriteText(w io.Writer, r Report) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "jbbench report\n")
	fmt.Fprintf(&b, "  frames=%d received=%d played=%d\n",
		r.Frames, r.Received, r.Played)
	fmt.Fprintf(&b, "  invalid_packets=%d (corrupt=%d bad_version=%d bad_length=%d ambiguous=%d)\n",
		r.Invalid(), r.Corrupt, r.BadVersion, r.BadLength, r.Ambiguous)
	fmt.Fprintf(&b, "  duplicate_drops=%d late_drops=%d overflow_drops=%d reordered_packets=%d\n",
		r.Duplicate, r.Late, r.Overflow, r.Reordered)
	fmt.Fprintf(&b, "  concealed_frames=%d max_conceal_run=%d max_recovery_jump=%d recovery_ratio=%.4f\n",
		r.ConcealedFrames, r.MaxConcealRun, r.MaxRecoveryJump, r.RecoveryRatio)
	fmt.Fprintf(&b, "  latency_us p50=%d p95=%d p99=%d max=%d\n",
		r.LatencyP50US, r.LatencyP95US, r.LatencyP99US, r.LatencyMaxUS)
	fmt.Fprintf(&b, "  buffer_level_curve_points=%d\n", len(r.BufferLevelCurve))
	preview := r.BufferLevelCurve
	if len(preview) > 24 {
		preview = preview[:24]
	}
	for _, p := range preview {
		fmt.Fprintf(&b, "    t=%8dms level=%2d\n", p.T.Milliseconds(), p.Level)
	}
	if len(r.BufferLevelCurve) > 24 {
		fmt.Fprintf(&b, "    ... %d more points (use --json for the full curve)\n",
			len(r.BufferLevelCurve)-24)
	}
	_, err := w.Write(b.Bytes())
	return err
}

// Validate enforces report invariants used by the internal consistency check.
func Validate(r Report) error {
	if r.Frames < r.Played {
		return Inconsistent("played %d exceeds frames %d", r.Played, r.Frames)
	}
	if r.ConcealedFrames > r.Played {
		return Inconsistent("concealed %d exceeds played %d",
			r.ConcealedFrames, r.Played)
	}
	if r.RecoveryRatio < 0 || r.RecoveryRatio > 1 {
		return Inconsistent("recovery ratio %.4f outside [0,1]", r.RecoveryRatio)
	}
	return nil
}
