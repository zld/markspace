package main

import (
	"flag"
	"fmt"
	"io"

	"mp4seek/internal/index"
	"mp4seek/internal/report"
)

type inspectTrack struct {
	TrackID   uint32 `json:"track_id"`
	Type      string `json:"type"`
	Codec     string `json:"codec"`
	Timescale uint32 `json:"timescale"`
	Samples   int    `json:"samples"`
	Edits     int    `json:"edits"`
	Encrypted bool   `json:"encrypted"`
}

type inspectOut struct {
	MovieTimescale  uint32         `json:"movie_timescale"`
	MovieDurationMs int64          `json:"movie_duration_ms"`
	Tracks          int            `json:"tracks"`
	VideoSamples    int            `json:"video_samples"`
	AudioSamples    int            `json:"audio_samples"`
	Fragmented      bool           `json:"fragmented"`
	TrackList       []inspectTrack `json:"track_list"`
}

func cmdInspect(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "input MP4 file")
	asJSON := fs.Bool("json", false, "emit JSON instead of key=value lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		fs.Usage()
		return fmt.Errorf("inspect: --input is required")
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

	out := inspectOut{
		MovieTimescale:  mv.Timescale,
		MovieDurationMs: int64(mv.Duration) * 1000 / int64(mv.Timescale),
		Tracks:          len(mv.Tracks),
		Fragmented:      len(mv.Moofs) > 0,
	}
	for _, ti := range mv.Tracks {
		it := inspectTrack{
			TrackID:   ti.ID,
			Type:      handlerName(ti.Handler),
			Timescale: ti.Timescale,
			Edits:     len(ti.Edits),
		}
		if ti.HasStbl {
			tbl, err := index.BuildClassic(r, ti.Stbl)
			if err != nil {
				return err
			}
			it.Codec = tbl.Entry.Format
			it.Encrypted = tbl.Entry.Encrypted
			it.Samples = len(tbl.Samples)
		}
		if it.Samples == 0 && len(mv.Moofs) > 0 {
			trex, err := index.ParseTrex(r, mv.Moov, ti.ID)
			if err != nil {
				return err
			}
			fidx, err := index.BuildFragmented(r, mv.Moofs, ti.ID, trex)
			if err != nil {
				return err
			}
			it.Samples = len(fidx.Samples)
		}
		switch ti.Handler {
		case "vide":
			out.VideoSamples += it.Samples
		case "soun":
			out.AudioSamples += it.Samples
		}
		out.TrackList = append(out.TrackList, it)
	}

	if *asJSON {
		return report.NewJSONL(stdout).Emit(out)
	}
	w := func(kv ...any) error { return report.KV(stdout, kv...) }
	if err := w("movie_timescale", out.MovieTimescale, "movie_duration_ms", out.MovieDurationMs,
		"tracks", out.Tracks, "video_samples", out.VideoSamples,
		"audio_samples", out.AudioSamples, "fragmented", out.Fragmented); err != nil {
		return err
	}
	for _, t := range out.TrackList {
		if err := w("track_id", t.TrackID, "type", t.Type, "codec", t.Codec,
			"timescale", t.Timescale, "samples", t.Samples, "edits", t.Edits); err != nil {
			return err
		}
	}
	return nil
}

func handlerName(h string) string {
	switch h {
	case "vide":
		return "video"
	case "soun":
		return "audio"
	case "":
		return "unknown"
	default:
		return h
	}
}
