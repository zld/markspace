// Package probe reads media segment durations with the FFmpeg 7.x ffprobe
// command. Every probe is bounded by a timeout and a concurrency limit, and
// failures are recorded per segment instead of aborting the run.
package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout is used when Options.Timeout is zero. ffprobe needs only a
// header read for the fixtures, so a few seconds is generous.
const DefaultTimeout = 5 * time.Second

// Options configures probing.
type Options struct {
	// Binary is the ffprobe executable; empty means "ffprobe" from PATH.
	Binary string
	// Timeout bounds each individual ffprobe invocation.
	Timeout time.Duration
	// Concurrency bounds how many ffprobe processes run at once.
	Concurrency int
}

// CommandError is a failed ffprobe invocation. Output is the last line of
// stderr, which never contains file contents, only diagnostics.
type CommandError struct {
	ExitCode int
	Output   string
}

func (e *CommandError) Error() string {
	if e.Output == "" {
		return fmt.Sprintf("ffprobe exited with status %d", e.ExitCode)
	}
	return fmt.Sprintf("ffprobe exited with status %d: %s", e.ExitCode, e.Output)
}

// Available reports whether the configured ffprobe binary can be executed.
func Available(binary string) error {
	if binary == "" {
		binary = "ffprobe"
	}
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("ffprobe not found (%q): install FFmpeg 7.x, e.g. brew install ffmpeg", binary)
	}
	return nil
}

// Duration returns the format duration of one media file in milliseconds,
// rounded to the nearest millisecond.
func Duration(ctx context.Context, binary, path string, timeout time.Duration) (int64, error) {
	if binary == "" {
		binary = "ffprobe"
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(pctx, binary,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"--", path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// After the context expires, stop waiting for inherited stdout/stderr
	// pipes held open by child processes, so the timeout really bounds us.
	cmd.WaitDelay = 200 * time.Millisecond
	err := cmd.Run()
	if pctx.Err() == context.DeadlineExceeded {
		return 0, fmt.Errorf("ffprobe timeout after %s", timeout)
	}
	if err != nil {
		out := lastLine(stderr.String())
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return 0, &CommandError{ExitCode: exitErr.ExitCode(), Output: out}
		}
		return 0, fmt.Errorf("ffprobe %q: %w", path, err)
	}
	raw := strings.TrimSpace(stdout.String())
	if raw == "" || raw == "N/A" {
		return 0, fmt.Errorf("ffprobe reported no duration")
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe returned unparseable duration %q", raw)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("ffprobe reported zero duration")
	}
	return int64(seconds*1000 + 0.5), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 200 {
		line = line[:200] + "..."
	}
	return line
}
