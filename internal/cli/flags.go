package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"

	"loudscan/internal/media"
	"loudscan/internal/report"
)

// Config is the parsed, validated command line for the scan command.
type Config struct {
	Inputs        []string
	IntegratedMax float64
	TruePeakMax   float64
	Workers       int
	Format        report.Format
	Layout        string
	Limits        media.Limits
	Timeout       time.Duration
	ProbePath     string
	FFmpegPath    string
}

// FixtureConfig configures the fixture subcommand.
type FixtureConfig struct {
	Dir  string
	Seed int64
}

// Usage errors are reported with exit code 2.
var ErrUsage = errors.New("usage error")

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("empty path")
	}
	*s = append(*s, v)
	return nil
}

// ParseScan parses scan arguments (without the subcommand name).
func ParseScan(args []string, stderr io.Writer) (Config, error) {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var inputs stringList
	fs.Var(&inputs, "input", "file or directory to scan; repeatable")
	integrated := fs.Float64("integrated-max", -16, "maximum integrated loudness in LUFS")
	peak := fs.Float64("true-peak-max", -1, "maximum true peak in dBTP")
	workers := fs.Int("workers", defaultWorkers(), "number of files processed concurrently (1-8)")
	format := fs.String("format", "table", "output format: table or json")
	layout := fs.String("layout", "", "channel labels for non-standard layouts, e.g. FL,FR,FC,LFE,BL,BR")
	maxBytes := fs.Int64("max-bytes", media.DefaultMaxBytes, "skip inputs larger than this many bytes")
	maxSeconds := fs.Float64("max-seconds", media.DefaultMaxSeconds, "skip inputs longer than this many seconds")
	timeout := fs.Duration("timeout", media.DefaultDecodeTimeout, "per-file decode timeout")
	probePath := fs.String("ffprobe", "ffprobe", "ffprobe executable")
	ffmpegPath := fs.String("ffmpeg", "ffmpeg", "ffmpeg executable")

	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return Config{}, fmt.Errorf("%w: unexpected argument %q", ErrUsage, rest[0])
	}

	cfg := Config{
		Inputs:        inputs,
		IntegratedMax: *integrated,
		TruePeakMax:   *peak,
		Workers:       *workers,
		Layout:        *layout,
		Limits:        media.Limits{MaxBytes: *maxBytes, MaxSeconds: *maxSeconds},
		Timeout:       *timeout,
		ProbePath:     *probePath,
		FFmpegPath:    *ffmpegPath,
	}
	if len(cfg.Inputs) == 0 {
		return Config{}, fmt.Errorf("%w: at least one --input is required", ErrUsage)
	}
	f, err := report.ParseFormat(*format)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrUsage, err)
	}
	cfg.Format = f
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Workers < 1 || c.Workers > 8 {
		return fmt.Errorf("%w: --workers must be between 1 and 8", ErrUsage)
	}
	if c.IntegratedMax < -70 || c.IntegratedMax > 0 {
		return fmt.Errorf("%w: --integrated-max must be between -70 and 0 LUFS", ErrUsage)
	}
	if c.TruePeakMax < -40 || c.TruePeakMax > 10 {
		return fmt.Errorf("%w: --true-peak-max must be between -40 and 10 dBTP", ErrUsage)
	}
	if c.Limits.MaxBytes <= 0 && c.Limits.MaxBytes != -1 {
		return fmt.Errorf("%w: --max-bytes must be positive or -1 to disable", ErrUsage)
	}
	if c.Limits.MaxSeconds <= 0 && c.Limits.MaxSeconds != -1 {
		return fmt.Errorf("%w: --max-seconds must be positive or -1 to disable", ErrUsage)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("%w: --timeout must be positive", ErrUsage)
	}
	return nil
}

// ParseFixture parses fixture arguments (without the subcommand name).
func ParseFixture(args []string, stderr io.Writer) (FixtureConfig, error) {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "fixtures", "directory to write the fixture WAVs into")
	seed := fs.Int64("seed", 17, "generator seed (recorded for reproducibility)")
	if err := fs.Parse(args); err != nil {
		return FixtureConfig{}, fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return FixtureConfig{}, fmt.Errorf("%w: unexpected argument %q", ErrUsage, rest[0])
	}
	if strings.TrimSpace(*dir) == "" {
		return FixtureConfig{}, fmt.Errorf("%w: --dir must not be empty", ErrUsage)
	}
	return FixtureConfig{Dir: *dir, Seed: *seed}, nil
}

func defaultWorkers() int {
	n := runtime.NumCPU()
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	return n
}

// SortInputs returns the inputs in a deterministic order before dispatch, so
// error messages about discovery are stable too.
func SortInputs(inputs []string) []string {
	out := append([]string(nil), inputs...)
	sort.Strings(out)
	return out
}
