// Command generate produces the deterministic sample video and the
// abnormal-input fixtures used by the demo and acceptance runs.
//
// Usage: go run ./testdata/generate.go -out sample.mp4 -duration 12 -fps 30
//        go run ./testdata/generate.go -fixtures ./testdata/out
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"transcode/internal/media"
)

func main() {
	out := flag.String("out", "", "output path for the sample video")
	duration := flag.Float64("duration", 12, "sample duration in seconds")
	fps := flag.Float64("fps", 30, "sample frame rate")
	fixtures := flag.String("fixtures", "", "directory for abnormal-input fixtures")
	flag.Parse()
	ctx := context.Background()
	if *fixtures != "" {
		if err := media.GenerateFixtures(ctx, *fixtures); err != nil {
			fmt.Fprintln(os.Stderr, "fixtures:", err)
			os.Exit(1)
		}
		fmt.Println("fixtures written to", *fixtures)
	}
	if *out != "" {
		if err := media.GenerateSample(ctx, *out, *duration, *fps); err != nil {
			fmt.Fprintln(os.Stderr, "sample:", err)
			os.Exit(1)
		}
		fmt.Println("sample written to", *out)
	}
	if *fixtures == "" && *out == "" {
		flag.Usage()
		os.Exit(2)
	}
}
