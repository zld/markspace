package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/zld/media-check/media"
)

func RunVerify(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	input := fs.String("input", "", "source file")
	output := fs.String("output", "", "remuxed file")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *input == "" || *output == "" {
		fmt.Fprintf(os.Stderr, "verify: --input and --output are required\n%s", Usage())
		return ExitUsage
	}
	_ = ctx
	if err := ctx.Err(); err != nil {
		return ExitCancelled
	}
	rep, err := verifyPair(*input, *output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return ExitCode(err)
	}
	fmt.Printf("roundtrip_parse=%.2f%%\n", rep.roundtrip*100)
	fmt.Printf("keyframe_hit=%.2f%%\n", rep.kfHit*100)
	fmt.Printf("tracks=%d\n", rep.tracks)
	fmt.Printf("segments=%d\n", rep.segments)
	fmt.Printf("sample_match=%t\n", rep.samplesMatch)
	fmt.Printf("tracks_detail=%s\n", rep.detail)
	return ExitOK
}

func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) {
		return ExitCancelled
	}
	switch media.ClassOf(err) {
	case media.ClassInvalidArgument, media.ClassMappingConflict:
		return ExitUsage
	case media.ClassCorruptPacket, media.ClassGap:
		return ExitCorrupt
	}
	return ExitCorrupt
}
