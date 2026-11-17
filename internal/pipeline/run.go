// Package pipeline orchestrates the full conversion: decode, parse, frame
// mapping, overlap resolution, serialization, atomic output and self-hosted
// round-trip verification.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"subtitle/internal/charset"
	"subtitle/internal/cue"
	"subtitle/internal/overlap"
	"subtitle/internal/parser"
	"subtitle/internal/report"
	"subtitle/internal/timebase"
)

// MaxFileSize bounds any single input file at 64 MiB.
const MaxFileSize = 64 * 1024 * 1024

// Options configures one conversion run.
type Options struct {
	InputPath  string
	OutputPath string
	FromFormat cue.Format // zero means detect from extension
	ToFormat   cue.Format
	Roundtrip  cue.Format
	Rate       timebase.Rate
	Strategy   string
	Encoding   string // explicit encoding override, may be empty
	Force      bool   // allow overwriting an existing output file
}

// Run converts a single file and returns its report row.
func Run(opts Options) report.FileResult {
	res := report.FileResult{Path: opts.InputPath}
	if samePath(opts.InputPath, opts.OutputPath) {
		res.Error = fmt.Sprintf("output path must differ from input path: %s", opts.InputPath)
		return res
	}
	raw, encName, err := readAndDecode(opts.InputPath, opts.Encoding)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Encoding = encName

	from := opts.FromFormat
	if from == "" {
		from = formatFromExt(opts.InputPath)
	}
	doc, err := parseInput(from, opts.InputPath, raw)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.InputCount = len(doc.Cues)

	items, err := mapFrames(doc.Cues, opts.Rate)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	outcomes, err := overlap.Resolve(items, opts.Strategy)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	serialized, outCues, cueReports, maxDelta, err :=
		buildOutput(doc.Cues, outcomes, opts.Rate, opts.ToFormat, opts.Roundtrip)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if err := atomicWrite(opts.OutputPath, []byte(serialized), opts.Force); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Cues = cueReports
	res.OutputCount = len(outCues)
	res.Dropped = overlap.Dropped(outcomes)
	res.MaxAbsDeltaMS = maxDelta
	return res
}

func readAndDecode(path, explicit string) (text, encName string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() > MaxFileSize {
		return "", "", fmt.Errorf("%s is %d bytes, exceeds 64 MiB limit", path, info.Size())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) > MaxFileSize {
		return "", "", fmt.Errorf("%s exceeds 64 MiB limit", path)
	}
	decoded, detected, err := charset.Decode(raw, explicit)
	if err != nil {
		return "", "", err
	}
	return string(decoded), detected.Name, nil
}

func formatFromExt(path string) cue.Format {
	ext := filepath.Ext(path)
	switch ext {
	case ".srt":
		return cue.FormatSRT
	case ".vtt":
		return cue.FormatVTT
	case ".ass":
		return cue.FormatASS
	default:
		return cue.Format(ext)
	}
}

func parseInput(format cue.Format, path, text string) (*cue.Document, error) {
	switch format {
	case cue.FormatSRT:
		return parser.ParseSRT(path, text)
	case cue.FormatVTT:
		return parser.ParseVTT(path, text)
	case cue.FormatASS:
		return parser.ParseASS(path, text)
	default:
		return nil, fmt.Errorf("cannot infer input format from %q; pass --from", path)
	}
}

func mapFrames(cues []cue.Cue, rate timebase.Rate) ([]overlap.Item, error) {
	items := make([]overlap.Item, len(cues))
	for i, c := range cues {
		f, err := rate.Map(c.Start, c.End)
		if err != nil {
			return nil, fmt.Errorf("cue %d (line %d): %w", i+1, c.SourceLine, err)
		}
		items[i] = overlap.Item{Index: i + 1, Frames: f}
	}
	return items, nil
}
