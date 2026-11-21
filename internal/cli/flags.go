// Package cli parses the command line shared by all loudcheck subcommands.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"

	"loudcheck/internal/fixture"
	"loudcheck/internal/policy"
	"loudcheck/internal/report"
)

// Exit codes documented for every command.
const (
	ExitOK       = 0 // every file measured and passed
	ExitFail     = 1 // measurement completed but a threshold was missed
	ExitUsage    = 2 // bad arguments or unreadable input
	ExitFileFail = 3 // at least one file failed to parse or analyse
)

// Options is the fully resolved configuration for one invocation.
type Options struct {
	Command string

	// Analysis settings.
	LayoutName string
	Mapping    string
	Format     report.Format
	Thresholds policy.Thresholds
	Workers    int
	Quiet      bool

	// Inputs.
	InputDir string
	Files    []string

	// synth settings.
	SynthOut string
	Synth    fixture.Options
}

// UsageError marks an error that should exit with ExitUsage.
type UsageError struct{ Err error }

// ErrHelp signals that the user asked for help. It is not a failure: the
// entry point prints HelpText and exits 0.
var ErrHelp = errors.New("help requested")

func (u UsageError) Error() string { return u.Err.Error() }
func (u UsageError) Unwrap() error { return u.Err }

func usagef(format string, args ...any) error {
	return UsageError{fmt.Errorf(format, args...)}
}

// Parse resolves argv (without the program name) into Options.
func Parse(argv []string, stderr io.Writer) (Options, error) {
	if len(argv) == 0 {
		return Options{}, usagef("missing command (want synth, analyze or batch)")
	}
	cmd := argv[0]
	switch cmd {
	case "synth", "analyze", "batch":
	case "-h", "--help", "help":
		return Options{}, ErrHelp
	default:
		return Options{}, usagef("unknown command %q (want synth, analyze or batch)", cmd)
	}

	o := Options{Command: cmd, Thresholds: policy.DefaultThresholds()}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {} // Parse reports usage through its error; main prints it.

	format := "text"
	var synth synthFlags
	if cmd == "synth" {
		parseSynth(fs, &o, &synth)
	} else {
		parseAnalysis(fs, &o)
	}
	fs.StringVar(&format, "format", "text", "output format: text or json")
	if cmd == "batch" {
		fs.StringVar(&o.InputDir, "input-dir", "", "directory scanned for .wav files (one level)")
	}

	// The flag package stops at the first positional argument. Files and
	// flags may be interleaved on the command line, so keep parsing after
	// each positional and collect them separately.
	var positional []string
	rest := argv[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return Options{}, ErrHelp
			}
			return Options{}, usagef("%v", err)
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	f, err := report.ParseFormat(format)
	if err != nil {
		return Options{}, usagef("%v", err)
	}
	o.Format = f
	if cmd == "batch" && o.InputDir == "" && len(positional) == 0 {
		return Options{}, usagef("batch needs files or --input-dir")
	}
	if err := o.Thresholds.Validate(); err != nil {
		return Options{}, usagef("%v", err)
	}

	if cmd == "synth" {
		return o, finishSynth(&o, &synth, positional)
	}
	return o, finishAnalysis(&o, positional)
}

// synthFlags holds the raw synth-only values that need conversion.
type synthFlags struct {
	encoding     string
	toneChannels int
	channels     int
}

func parseAnalysis(fs *flag.FlagSet, o *Options) {
	fs.StringVar(&o.LayoutName, "layout", "", "mono, stereo, 5.1, 7.1, or auto (default: infer)")
	fs.StringVar(&o.Mapping, "map", "", "explicit per-channel weights, e.g. 1,1,1,0,1.41,1.41")
	fs.Float64Var(&o.Thresholds.TargetLUFS, "target-lufs", o.Thresholds.TargetLUFS, "target integrated loudness in LUFS")
	fs.Float64Var(&o.Thresholds.ToleranceLU, "tolerance-lu", o.Thresholds.ToleranceLU, "allowed deviation from the target in LU")
	fs.Float64Var(&o.Thresholds.MaxTruePeakDBTP, "max-true-peak-dbtp", o.Thresholds.MaxTruePeakDBTP, "true-peak ceiling in dBTP")
	fs.IntVar(&o.Workers, "workers", runtime.NumCPU(), "parallel files analysed at once (1..64)")
	fs.BoolVar(&o.Quiet, "quiet", false, "suppress the threshold banner")
}

func parseSynth(fs *flag.FlagSet, o *Options, s *synthFlags) {
	fs.StringVar(&o.SynthOut, "out", "", "output WAV path (required)")
	fs.StringVar(&o.LayoutName, "layout", "stereo", "mono, stereo, 5.1, 7.1, or custom")
	fs.IntVar(&s.channels, "channels", 0, "channel count for --layout custom")
	fs.Float64Var(&o.Synth.Seconds, "seconds", 12, "duration in seconds")
	fs.Float64Var(&o.Synth.ToneDBFS, "tone-dbfs", -23, "sine amplitude in dBFS")
	fs.Float64Var(&o.Synth.ToneHz, "tone-hz", 997, "sine frequency in Hz")
	fs.IntVar(&o.Synth.SampleRate, "sample-rate", 48000, "sample rate in Hz")
	fs.StringVar(&s.encoding, "encoding", "pcm24", "pcm16, pcm24, pcm32 or float32")
	fs.BoolVar(&o.Synth.Extensible, "extensible", true, "write WAVE_FORMAT_EXTENSIBLE headers")
	fs.IntVar(&s.toneChannels, "tone-channels", 0, "leading channels carrying the tone (0 = all)")
	fs.BoolVar(&o.Synth.DataFirst, "data-first", false, "write the data chunk before fmt (parser test)")
	fs.BoolVar(&o.Synth.OddJunk, "odd-junk", false, "insert an odd-sized unknown chunk (parser test)")
	fs.Float64Var(&o.Thresholds.TargetLUFS, "target-lufs", o.Thresholds.TargetLUFS, "target loudness in LUFS for verification")
	fs.Float64Var(&o.Thresholds.ToleranceLU, "tolerance-lu", o.Thresholds.ToleranceLU, "allowed deviation from the target in LU")
	fs.Float64Var(&o.Thresholds.MaxTruePeakDBTP, "max-true-peak-dbtp", o.Thresholds.MaxTruePeakDBTP, "true-peak ceiling in dBTP")
}

func finishSynth(o *Options, s *synthFlags, args []string) error {
	if len(args) > 0 {
		return usagef("synth takes no positional arguments (got %d)", len(args))
	}
	if o.SynthOut == "" {
		return usagef("synth requires --out")
	}
	if o.Synth.Seconds <= 0 || o.Synth.Seconds > 3600 {
		return usagef("--seconds must be in (0, 3600]")
	}
	if o.Synth.SampleRate < 8000 || o.Synth.SampleRate > 384000 {
		return usagef("--sample-rate must be between 8000 and 384000")
	}
	if o.Synth.ToneHz <= 0 || o.Synth.ToneHz > float64(o.Synth.SampleRate)/2 {
		return usagef("--tone-hz must be between 0 and Nyquist")
	}
	enc, err := fixture.ParseEncoding(s.encoding)
	if err != nil {
		return usagef("%v", err)
	}
	o.Synth.Encoding = enc

	channels, err := synthChannels(o.LayoutName, s.channels)
	if err != nil {
		return err
	}
	o.Synth.Channels = channels
	if s.toneChannels < 0 || s.toneChannels > channels {
		return usagef("--tone-channels must be between 0 and %d", channels)
	}
	if s.toneChannels > 0 {
		o.Synth.ToneChannels = make([]int, s.toneChannels)
		for i := range o.Synth.ToneChannels {
			o.Synth.ToneChannels[i] = i
		}
	}
	return nil
}

// synthChannels resolves the channel count a layout name implies. "custom"
// takes the count from --channels so a layout the meter only knows through
// --map can still be generated.
func synthChannels(name string, custom int) (int, error) {
	switch name {
	case "mono", "":
		return 1, nil
	case "stereo", "auto":
		return 2, nil
	case "5.1", "5.1(side)":
		return 6, nil
	case "7.1":
		return 8, nil
	case "custom":
		if custom < 1 || custom > 64 {
			return 0, usagef("--layout custom requires --channels between 1 and 64")
		}
		return custom, nil
	}
	return 0, usagef("unknown synth layout %q", name)
}

func finishAnalysis(o *Options, args []string) error {
	o.Files = args
	if o.InputDir == "" && len(args) == 0 {
		return usagef("no input: pass file paths or --input-dir")
	}
	if o.InputDir != "" && len(args) > 0 {
		return usagef("--input-dir cannot be combined with positional files")
	}
	if o.Command == "analyze" && len(args) != 1 {
		return usagef("analyze takes exactly one file")
	}
	if o.Workers < 1 || o.Workers > 64 {
		return usagef("--workers must be between 1 and 64")
	}
	return nil
}

// HelpText is printed for --help and on usage errors.
const HelpText = `loudcheck - offline loudness and true-peak meter

usage:
  loudcheck synth   --out FILE [--layout L] [--seconds S] [--tone-dbfs D]
                    [--tone-hz F] [--sample-rate R] [--encoding E]
                    [--extensible] [--tone-channels N] [--channels N]
                    [--data-first] [--odd-junk] [--format text|json]
  loudcheck analyze FILE [--layout L] [--map W,W,...] [--target-lufs T]
                    [--tolerance-lu U] [--max-true-peak-dbtp P]
                    [--format text|json] [--quiet]
  loudcheck batch   [FILES|DIRS...] [--input-dir DIR] [--workers N]
                    [analysis flags]

analysis flags default to: --target-lufs -23 --tolerance-lu 1
--max-true-peak-dbtp -1 --layout auto --format text
batch defaults to --workers equal to the CPU count (clamped to 1..64).

synth defaults to: --layout stereo --seconds 12 --tone-dbfs -23 --tone-hz 997
--sample-rate 48000 --encoding pcm24 --extensible. synth verifies its own
output with the same analyzer and thresholds as analyze, and exits with the
same codes.

exit codes:
  0  all files measured and passed
  1  measurement completed, at least one threshold failed
  2  bad arguments, unreadable input, or a layout problem
  3  at least one file failed to parse or analyse

layouts:
  mono, stereo, 5.1 and 7.1 are recognized. Any other channel count needs an
  explicit --map with one weight per channel; LFE channels use weight 0.
  Surround channels use the BS.1770 1.41 weight and are not folded to front.
`
