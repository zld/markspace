package main

import (
	"fmt"
	"io"
	"os"

	"example.com/assetcheck/internal/report"
	"example.com/assetcheck/internal/scan"
)

// runCheck validates explicit input files.
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("check")
	mp4Path := fs.String("mp4", "", "MP4 asset to check")
	rtpPath := fs.String("rtp", "", "length-prefixed RTP packet stream")
	idxPath := fs.String("index", "", "index path to reuse or rebuild (optional)")
	reuse := fs.Bool("reuse-index", false, "reuse the index at --index when its digest still matches")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "assetcheck check: %v\n%s\n", err, usage)
		return exitErr
	}
	if *mp4Path == "" || *rtpPath == "" {
		fmt.Fprintf(stderr, "assetcheck check: --mp4 and --rtp are required\n%s\n", usage)
		return exitErr
	}
	mp4, err := os.ReadFile(*mp4Path)
	if err != nil {
		fmt.Fprintf(stderr, "assetcheck check: %v\n", err)
		return exitErr
	}
	rtp, err := os.ReadFile(*rtpPath)
	if err != nil {
		fmt.Fprintf(stderr, "assetcheck check: %v\n", err)
		return exitErr
	}
	run, err := process(*mp4Path, scan.Input{MP4: mp4, RTP: rtp}, *idxPath, *reuse && *idxPath != "")
	if err != nil {
		fmt.Fprintf(stderr, "assetcheck check: %v\n", err)
		return exitErr
	}
	if err := report.Write(stdout, run); err != nil {
		fmt.Fprintf(stderr, "assetcheck check: %v\n", err)
		return exitErr
	}
	switch run.Verdict() {
	case report.Pass:
		return exitPass
	case report.Fail:
		return exitFail
	default:
		return exitErr
	}
}
