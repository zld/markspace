// Package codec runs the externally configured encoder as a local
// subprocess and verifies its output. The encoder command is always
// supplied explicitly by the caller; this package never searches PATH
// beyond the executable name the user configured and never invokes a
// shell, so argument injection through filenames is impossible.
package codec

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"transcode/internal/media"
	"transcode/internal/model"
)

// DefaultTolerancePct is the allowed bitrate deviation per segment.
const DefaultTolerancePct = 5.0

// ErrOutputTooLarge means the encoder produced more bytes than the
// configured per-segment ceiling.
var ErrOutputTooLarge = errors.New("encoder output exceeds per-segment limit")

// Spec describes one encode invocation.
type Spec struct {
	CommandTemplate string // e.g. "/path/stubencoder --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration}"
	InputPath       string
	OutputPath      string
	TargetKbps      float64
	DurationSec     float64
	MaxOutputBytes  int64
}

// Runner executes encoder commands.
type Runner struct {
	tolerancePct float64
}

// NewRunner builds a runner with the standard 5% deviation tolerance.
func NewRunner() *Runner { return &Runner{tolerancePct: DefaultTolerancePct} }

// expand replaces the fixed placeholder set. Unknown placeholders and
// shell metacharacters in values are not interpreted: values are passed as
// discrete argv entries.
func expand(template string, s Spec, effectiveKbps float64) ([]string, error) {
	repl := map[string]string{
		"{in}":       s.InputPath,
		"{out}":      s.OutputPath,
		"{kbps}":     strconv.FormatFloat(effectiveKbps, 'f', 3, 64),
		"{duration}": strconv.FormatFloat(s.DurationSec, 'f', 3, 64),
	}
	fields := strings.Fields(template)
	if len(fields) == 0 {
		return nil, errors.New("empty encoder command template")
	}
	args := make([]string, len(fields))
	for i, f := range fields {
		if v, ok := repl[f]; ok {
			args[i] = v
			continue
		}
		if strings.Contains(f, "{") || strings.Contains(f, "}") {
			return nil, fmt.Errorf("unsupported encoder placeholder in %q", f)
		}
		args[i] = f
	}
	if !containsPlaceholder(fields, "{in}") || !containsPlaceholder(fields, "{out}") {
		return nil, errors.New("encoder template must include {in} and {out}")
	}
	return args, nil
}

func containsPlaceholder(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func (r *Runner) runOnce(ctx context.Context, s Spec, effectiveKbps float64) (*model.EncodeResult, error) {
	argv, err := expand(s.CommandTemplate, s, effectiveKbps)
	if err != nil {
		return &model.EncodeResult{OutputPath: s.OutputPath, TargetKbps: s.TargetKbps,
			FailureNote: "invalid encoder command: " + err.Error()}, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("start encoder: %w", err)
		}
	}
	if exitCode != 0 {
		return &model.EncodeResult{
			OutputPath: s.OutputPath, TargetKbps: s.TargetKbps,
			ActualKbps: 0, ExitCode: exitCode,
			FailureNote: truncateNote(string(out)),
		}, fmt.Errorf("encoder exited %d: %s", exitCode, truncateNote(string(out)))
	}

	// Verify existence, readability (truncation fails inside ReadEncoded),
	// size ceiling and actual bitrate.
	enc, err := media.ReadEncoded(s.OutputPath)
	if err != nil {
		return &model.EncodeResult{OutputPath: s.OutputPath, TargetKbps: s.TargetKbps,
			ExitCode: 0, FailureNote: "output verification failed: " + err.Error()}, err
	}
	st, err := statFile(s.OutputPath)
	if err != nil {
		return nil, err
	}
	if s.MaxOutputBytes > 0 && st.Size() > s.MaxOutputBytes {
		return &model.EncodeResult{OutputPath: s.OutputPath, Bytes: st.Size(),
				TargetKbps: s.TargetKbps, ExitCode: 0,
				FailureNote: fmt.Sprintf("%s: %d > %d bytes", ErrOutputTooLarge, st.Size(), s.MaxOutputBytes)},
			ErrOutputTooLarge
	}
	actualKbps := float64(st.Size()) * 8 / 1000 / s.DurationSec
	dev := (actualKbps - s.TargetKbps) / s.TargetKbps * 100
	res := &model.EncodeResult{
		OutputPath: s.OutputPath, Bytes: st.Size(), ActualKbps: actualKbps,
		TargetKbps: s.TargetKbps, Deviation: dev, ExitCode: 0,
	}
	_ = enc // frame count validation already happened in ReadEncoded
	return res, nil
}

// Run encodes one segment, retrying once when the bitrate deviates beyond
// tolerance. The retry retargets the encoder using the remaining budget:
// effective_kbps = target^2 / actual, which corrects a proportional miss.
// Nonzero exits, truncation and oversize outputs are returned as failure
// records rather than panics, so the scheduler can keep processing others.
func (r *Runner) Run(ctx context.Context, s Spec) (*model.EncodeResult, error) {
	first, err := r.runOnce(ctx, s, s.TargetKbps)
	attempts := 1
	if err != nil {
		first.Attempts = attempts
		return first, err
	}
	if math.Abs(first.Deviation) <= r.tolerancePct {
		first.Attempts = attempts
		return first, nil
	}

	// One budget-aware retry.
	retryKbps := s.TargetKbps * s.TargetKbps / first.ActualKbps
	second, err2 := r.runOnce(ctx, s, retryKbps)
	attempts = 2
	if err2 != nil {
		second.Attempts = attempts
		second.FailureNote = fmt.Sprintf("retry after %.2f%% deviation failed: %s",
			first.Deviation, second.FailureNote)
		return second, err2
	}
	second.Attempts = attempts
	if math.Abs(second.Deviation) > r.tolerancePct {
		second.FailureNote = fmt.Sprintf(
			"bitrate %.2f kbps deviates %.2f%% from target %.2f after one retry",
			second.ActualKbps, second.Deviation, s.TargetKbps)
		return second, errors.New(second.FailureNote)
	}
	return second, nil
}

func truncateNote(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func statFile(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
