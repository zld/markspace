// Command transcode is the offline transcoding job scheduler. It plans
// per-segment bitrates, drives an explicitly configured local encoder,
// measures PSNR, checkpoints progress atomically and emits a text or JSON
// report with documented exit codes.
//
// Exit codes:
//
//	0  all segments succeeded
//	1  unexpected internal error
//	2  invalid parameters or input
//	3  one or more segments failed / run interrupted
//	4  checkpoint corrupt or unrecoverable
package main

import (
	"fmt"
	"os"

	"transcode/internal/cli"
)

func main() {
	opts, err := cli.Parse(os.Args[1:])
	if err != nil {
		if cli.IsInvalid(err) {
			fmt.Fprintf(os.Stderr, "transcode: %v\n", err)
			os.Exit(cli.ExitInvalidInput)
		}
		// flag package already printed usage for parse errors.
		fmt.Fprintf(os.Stderr, "transcode: %v\n", err)
		os.Exit(cli.ExitGeneric)
	}
	os.Exit(cli.NewApp().Run(opts))
}
