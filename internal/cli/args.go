// Package cli parses terminal arguments for the fixture and verify commands.
// Exit codes are centralised here so the entry point stays a thin dispatcher:
//
//	0  success (all verifications passed)
//	1  subtitle content or round-trip verification failure
//	2  parameter, encoding or file handling error
//	3  batch run with one or more failed files
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"subtitle/internal/cue"
	"subtitle/internal/overlap"
	"subtitle/internal/pipeline"
	"subtitle/internal/timebase"
)

// Exit codes returned to the operating system.
const (
	ExitOK             = 0
	ExitVerifyFailure  = 1
	ExitUsageOrIOError = 2
	ExitPartialBatch   = 3
)

// VerifyArgs holds every parsed option of a verify command.
type VerifyArgs struct {
	Input     string
	Output    string
	From      cue.Format
	To        cue.Format
	Roundtrip cue.Format
	Rate      timebase.Rate
	Strategy  string
	Encoding  string
	Jobs      int
	Force     bool
	IsBatch   bool
}

// UsageError marks argument problems so main can map them to exit code 2.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

func usageError(format string, args ...any) error {
	return &UsageError{Msg: fmt.Sprintf(format, args...)}
}

// ParseVerify parses arguments following the "verify" subcommand.
func ParseVerify(args []string) (VerifyArgs, error) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		input     = fs.String("input", "", "input .srt/.vtt/.ass file or directory")
		output    = fs.String("output", "", "output file (single-file mode)")
		from      = fs.String("from", "", "source format override (srt|vtt|ass)")
		to        = fs.String("to", "", "target format (srt|vtt|ass)")
		roundtrip = fs.String("roundtrip", "", "second-leg format to re-serialize and verify")
		fps       = fs.String("fps", "24000/1001", "rational frame rate, e.g. 24000/1001 or 25")
		strategy  = fs.String("overlap", overlap.Keep, "keep|trim|drop-later")
		encoding  = fs.String("encoding", "", "force input encoding (utf-8|gbk|shift-jis)")
		jobs      = fs.Int("jobs", 1, "bounded worker count for directory batches")
		force     = fs.Bool("force", false, "allow overwriting an existing output file")
	)
	if err := fs.Parse(args); err != nil {
		return VerifyArgs{}, usageError("%v", err)
	}
	if *input == "" {
		return VerifyArgs{}, usageError("--input is required")
	}
	if *to == "" {
		return VerifyArgs{}, usageError("--to is required")
	}
	toFmt, err := cue.ParseFormat(*to)
	if err != nil {
		return VerifyArgs{}, usageError("%v", err)
	}
	var fromFmt cue.Format
	if *from != "" {
		if fromFmt, err = cue.ParseFormat(*from); err != nil {
			return VerifyArgs{}, usageError("%v", err)
		}
	}
	var rtFmt cue.Format
	if *roundtrip != "" {
		if rtFmt, err = cue.ParseFormat(*roundtrip); err != nil {
			return VerifyArgs{}, usageError("%v", err)
		}
	}
	rate, err := timebase.ParseRate(*fps)
	if err != nil {
		return VerifyArgs{}, usageError("%v", err)
	}
	strat, err := overlap.ParseStrategy(*strategy)
	if err != nil {
		return VerifyArgs{}, usageError("%v", err)
	}
	if *jobs < 1 {
		return VerifyArgs{}, usageError("--jobs must be at least 1")
	}
	if err := validateEncoding(*encoding); err != nil {
		return VerifyArgs{}, err
	}
	va := VerifyArgs{
		Input:     *input,
		Output:    *output,
		From:      fromFmt,
		To:        toFmt,
		Roundtrip: rtFmt,
		Rate:      rate,
		Strategy:  strat,
		Encoding:  *encoding,
		Jobs:      *jobs,
		Force:     *force,
	}
	if *output == "" {
		va.IsBatch = true
	}
	return va, nil
}

func validateEncoding(name string) error {
	switch name {
	case "", "utf-8", "gbk", "shift-jis", "sjis":
		return nil
	default:
		return usageError("unsupported --encoding %q", name)
	}
}

// AsPipelineOptions projects parsed args onto pipeline options for one file.
func (va VerifyArgs) AsPipelineOptions(input, output string) pipeline.Options {
	return pipeline.Options{
		InputPath:  input,
		OutputPath: output,
		FromFormat: va.From,
		ToFormat:   va.To,
		Roundtrip:  va.Roundtrip,
		Rate:       va.Rate,
		Strategy:   va.Strategy,
		Encoding:   va.Encoding,
		Force:      va.Force,
	}
}

// IsUsage reports whether err originates from bad command line arguments.
func IsUsage(err error) bool {
	var ue *UsageError
	return errors.As(err, &ue)
}

// runHelp writes the help text and returns the success exit code.
func runHelp(w io.Writer) int {
	fmt.Fprint(w, HelpText)
	return ExitOK
}

// HelpText is printed on request or when arguments are invalid.
const HelpText = `subtitle - frame-accurate subtitle conversion tool

Usage:
  subtitle fixture --dir DIR
  subtitle verify --input FILE --output FILE --to FMT [flags]
  subtitle verify --input DIR --to FMT [flags]

Flags (verify):
  --from string       source format override: srt|vtt|ass
  --to string         target format: srt|vtt|ass (required)
  --roundtrip string  re-serialize to this format and verify the second leg
  --fps string        rational frame rate, e.g. 24000/1001 or 25 (default 24000/1001)
  --overlap string    keep|trim|drop-later (default keep)
  --encoding string   force input encoding: utf-8|gbk|shift-jis
  --jobs int          bounded workers for directory batches (default 1)
  --force             overwrite an existing output file

Exit codes: 0 ok, 1 verification failure, 2 usage/encoding/file error, 3 partial batch failure
`
