package main

import (
	"flag"
	"fmt"
	"os"

	"mediaprep/internal/app"
	"mediaprep/internal/model"
)

func runInspect(args []string) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	path := fs.String("file", "", "local MP4 or MXF file to inspect")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "--file is required")
		return 1
	}
	rep, err := app.InspectFile(*path)
	if err != nil {
		printError(err)
		return model.InputExitCode(err)
	}
	fmt.Printf("file=%s format=%s size=%d tracks=%d\n", rep.File.Path, rep.File.Format, rep.File.Size, len(rep.File.Tracks))
	for _, tr := range rep.File.Tracks {
		fmt.Printf("track=%s role=%s codec=%s clock=%s samples=%d duration=%d\n",
			tr.Label, tr.Role, tr.Codec, tr.Clock.TickDuration.String(), len(tr.Samples), tr.Duration)
	}
	for _, md := range rep.Metadata {
		fmt.Printf("metadata track=%s rotation=%d color=%q conflicts=%d\n",
			md.Track, md.Rotation, md.Color, len(md.Conflicts))
		if md.Crop != nil {
			fmt.Printf("crop=x:%d,y:%d,w:%d,h:%d\n", md.Crop.X, md.Crop.Y, md.Crop.Width, md.Crop.Height)
		}
	}
	fmt.Println("inspect_verdict=OK")
	return 0
}
