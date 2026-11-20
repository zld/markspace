// Package cli parses and validates command-line arguments.
package cli

import (
	"flag"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"subx/internal/batch"
	"subx/internal/charset"
	"subx/internal/format"
	"subx/internal/timeline"
)

// UsageError reports invalid parameters (exit code 2).
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Options carries validated arguments for one subcommand.
type Options struct {
	Cmd       string // fixture, convert, batch
	Dir       string // fixture target dir
	Input     string
	Output    string
	InputDir  string
	OutputDir string
	Report    string
	Batch     batch.Options
}

const usage = `usage:
  subx fixture --dir DIR
  subx convert --input FILE --output FILE [--from srt|vtt] [--to srt|vtt]
               [--fps N|N/D] [--overlap keep|trim|error] [--encoding ENC] [--report FILE]
  subx batch --input-dir DIR --output-dir DIR --to srt|vtt
             [--fps N|N/D] [--overlap keep|trim|error] [--encoding ENC] [--jobs N] [--report FILE]`

// commonFlags are the conversion flags shared by convert and batch.
type commonFlags struct {
	from, to, fps, overlap, encoding string
}

func addCommon(fs *flag.FlagSet, c *commonFlags) {
	fs.StringVar(&c.from, "from", "", "input format: srt or vtt (default: infer from extension)")
	fs.StringVar(&c.to, "to", "", "output format: srt or vtt")
	fs.StringVar(&c.fps, "fps", "24", "frame rate: integer N or rational N/D")
	fs.StringVar(&c.overlap, "overlap", "keep", "overlap policy: keep, trim or error")
	fs.StringVar(&c.encoding, "encoding", "", "input encoding: utf-8, utf-16le, utf-16be, gb18030, gbk, windows-1252 (default: sniff)")
}

func (c *commonFlags) validate() (batch.Options, error) {
	var b batch.Options
	var err error
	if b.FPS, err = timeline.ParseFPS(c.fps); err != nil {
		return b, err
	}
	if b.Overlap, err = timeline.ParsePolicy(c.overlap); err != nil {
		return b, err
	}
	if c.encoding != "" {
		if !charset.Supported(c.encoding) {
			return b, &UsageError{Msg: fmt.Sprintf("unsupported encoding %q", c.encoding)}
		}
		b.Encoding = c.encoding
	}
	if b.From, err = parseFormat(c.from, "--from"); err != nil {
		return b, err
	}
	if b.To, err = parseFormat(c.to, "--to"); err != nil {
		return b, err
	}
	return b, nil
}

// Parse validates args and returns ready-to-use options.
func Parse(args []string) (Options, error) {
	o, err := parse(args)
	if err != nil {
		if _, ok := err.(*UsageError); !ok {
			err = &UsageError{Msg: err.Error()}
		}
		return Options{}, err
	}
	return o, nil
}

func parse(args []string) (Options, error) {
	if len(args) == 0 {
		return Options{}, &UsageError{Msg: "missing subcommand\n" + usage}
	}
	var o Options
	switch args[0] {
	case "fixture":
		o.Cmd = "fixture"
		fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
		fs.StringVar(&o.Dir, "dir", "", "output directory for sample files")
		if err := fs.Parse(args[1:]); err != nil {
			return o, err
		}
		if o.Dir == "" {
			return o, &UsageError{Msg: "fixture: --dir is required"}
		}
	case "convert":
		o.Cmd = "convert"
		var c commonFlags
		fs := flag.NewFlagSet("convert", flag.ContinueOnError)
		fs.StringVar(&o.Input, "input", "", "input subtitle file")
		fs.StringVar(&o.Output, "output", "", "output subtitle file")
		fs.StringVar(&o.Report, "report", "", "write JSON report to this file")
		addCommon(fs, &c)
		if err := fs.Parse(args[1:]); err != nil {
			return o, err
		}
		if o.Input == "" || o.Output == "" {
			return o, &UsageError{Msg: "convert: --input and --output are required"}
		}
		b, err := c.validate()
		if err != nil {
			return o, err
		}
		if b.To == "" {
			if b.To = inferFormat(o.Output); b.To == "" {
				return o, &UsageError{Msg: "convert: cannot infer output format; pass --to srt or --to vtt"}
			}
		}
		o.Batch = b
	case "batch":
		o.Cmd = "batch"
		var c commonFlags
		fs := flag.NewFlagSet("batch", flag.ContinueOnError)
		fs.StringVar(&o.InputDir, "input-dir", "", "directory of input subtitles")
		fs.StringVar(&o.OutputDir, "output-dir", "", "directory for converted outputs")
		fs.StringVar(&o.Report, "report", "", "write JSON report to this file")
		jobs := fs.Int("jobs", runtime.NumCPU(), "max concurrent conversions")
		addCommon(fs, &c)
		if err := fs.Parse(args[1:]); err != nil {
			return o, err
		}
		if o.InputDir == "" || o.OutputDir == "" || c.to == "" {
			return o, &UsageError{Msg: "batch: --input-dir, --output-dir and --to are required"}
		}
		if *jobs < 1 || *jobs > 64 {
			return o, &UsageError{Msg: "batch: --jobs must be between 1 and 64"}
		}
		b, err := c.validate()
		if err != nil {
			return o, err
		}
		b.Jobs = *jobs
		o.Batch = b
	default:
		return o, &UsageError{Msg: fmt.Sprintf("unknown subcommand %q\n%s", args[0], usage)}
	}
	return o, nil
}

func parseFormat(v, name string) (format.Format, error) {
	switch format.Format(v) {
	case "":
		return "", nil
	case format.SRT, format.VTT:
		return format.Format(v), nil
	}
	return "", &UsageError{Msg: fmt.Sprintf("%s must be srt or vtt, got %q", name, v)}
}

func inferFormat(path string) format.Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".srt":
		return format.SRT
	case ".vtt":
		return format.VTT
	}
	return ""
}
