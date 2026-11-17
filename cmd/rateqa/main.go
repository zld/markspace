// Command rateqa performs per-segment bitrate fitting and quality review for
// mpeg4 encodes of 8-bit YUV420p Y4M clips. See internal/cli for exit codes.
package main

import (
	"context"
	"os"

	"rateqa/internal/app"
	"rateqa/internal/cli"
)

func main() {
	opts, err := cli.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(cli.ExitUsage)
	}
	os.Exit(app.Run(context.Background(), opts))
}
