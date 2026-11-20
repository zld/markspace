package main

import (
	"flag"
	"fmt"
	"io"

	"mp4seek/internal/index"
	"mp4seek/internal/report"
)

type indexSummary struct {
	Kind       string `json:"kind"`
	TrackID    uint32 `json:"track_id"`
	Mode       string `json:"mode"`
	Timescale  uint32 `json:"timescale"`
	Samples    int    `json:"samples"`
	WrapEvents int    `json:"wrap_events"`
	MaxDTS     int64  `json:"max_dts"`
}

type indexSample struct {
	Kind   string `json:"kind"`
	Sample int    `json:"sample"`
	DTS    int64  `json:"dts"`
	PTS    int64  `json:"pts"`
	Offset int64  `json:"offset"`
	Size   uint32 `json:"size"`
	Key    bool   `json:"key"`
}

func cmdIndex(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "input MP4 file")
	track := fs.Uint("track", 0, "track ID")
	_ = fs.Bool("jsonl", false, "emit JSONL (the only format)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *track == 0 {
		fs.Usage()
		return fmt.Errorf("index: --input and --track are required")
	}
	r, f, err := openInput(*input)
	if err != nil {
		return err
	}
	defer f.Close()
	mv, err := loadMovie(r)
	if err != nil {
		return err
	}
	ti, err := mv.track(uint32(*track))
	if err != nil {
		return err
	}
	out := report.NewJSONL(stdout)

	if len(mv.Moofs) > 0 {
		trex, err := index.ParseTrex(r, mv.Moov, ti.ID)
		if err != nil {
			return err
		}
		fidx, err := index.BuildFragmented(r, mv.Moofs, ti.ID, trex)
		if err != nil {
			return err
		}
		if err := out.Emit(indexSummary{Kind: "summary", TrackID: ti.ID, Mode: "fragmented",
			Timescale: ti.Timescale, Samples: len(fidx.Samples),
			WrapEvents: fidx.WrapEvents, MaxDTS: fidx.MaxDTS}); err != nil {
			return err
		}
		for _, s := range fidx.Samples {
			if err := out.Emit(indexSample{Kind: "sample", Sample: s.Index, DTS: s.DTS,
				PTS: s.PTS, Offset: s.Offset, Size: s.Size, Key: s.Key}); err != nil {
				return err
			}
		}
		return nil
	}

	if !ti.HasStbl {
		return fmt.Errorf("%w: track %d has no sample table", errUnsupported, ti.ID)
	}
	tbl, err := index.BuildClassic(r, ti.Stbl)
	if err != nil {
		return err
	}
	var maxDTS int64
	for _, s := range tbl.Samples {
		if s.DTS > maxDTS {
			maxDTS = s.DTS
		}
	}
	if err := out.Emit(indexSummary{Kind: "summary", TrackID: ti.ID, Mode: "classic",
		Timescale: ti.Timescale, Samples: len(tbl.Samples), MaxDTS: maxDTS}); err != nil {
		return err
	}
	for _, s := range tbl.Samples {
		if err := out.Emit(indexSample{Kind: "sample", Sample: s.Index, DTS: s.DTS,
			PTS: s.PTS, Offset: s.Offset, Size: s.Size, Key: s.Key}); err != nil {
			return err
		}
	}
	return nil
}
