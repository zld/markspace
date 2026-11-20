package main

import (
	"flag"
	"fmt"
	"io"

	"mp4seek/internal/export"
	"mp4seek/internal/report"
)

func cmdExport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "input MP4 file")
	track := fs.Uint("track", 0, "track ID")
	fromMs := fs.Int64("from-ms", -1, "start of the half-open interval, ms")
	toMs := fs.Int64("to-ms", -1, "end of the half-open interval, ms")
	out := fs.String("out", "", "output JSONL manifest path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *track == 0 || *fromMs < 0 || *toMs <= *fromMs || *out == "" {
		fs.Usage()
		return fmt.Errorf("export: --input, --track, --from-ms, --to-ms and --out are required (from < to)")
	}
	r, f, err := openInput(*input)
	if err != nil {
		return err
	}
	defer f.Close()
	// Re-parse the input from scratch; BuildClassic validates that every
	// sample range lies inside the file and does not overlap.
	mv, err := loadMovie(r)
	if err != nil {
		return err
	}
	_, _, lengthSize, presented, err := prepareTrack(r, mv, uint32(*track))
	if err != nil {
		return err
	}
	from, err := msToTicks(*fromMs, mv.Timescale)
	if err != nil {
		return err
	}
	to, err := msToTicks(*toMs, mv.Timescale)
	if err != nil {
		return err
	}

	var records []export.Record
	for _, ps := range presented {
		if ps.MovieTicks < from || ps.MovieTicks >= to {
			continue // half-open [from, to)
		}
		rec, err := export.ReadSample(r, export.Input{
			Index:      ps.Index,
			Offset:     ps.Offset,
			Size:       ps.Size,
			PTSTicks:   ps.PTS,
			MoviePTSMs: ps.MovieMs,
		}, lengthSize)
		if err != nil {
			return err
		}
		records = append(records, rec)
	}
	if len(records) == 0 {
		return fmt.Errorf("%w: no samples in [%d,%d) ms", errOutOfRange, *fromMs, *toMs)
	}
	if err := export.WriteManifest(*out, records); err != nil {
		return err
	}
	return report.KV(stdout, "exported", len(records), "from_ms", *fromMs, "to_ms", *toMs, "out", *out)
}
