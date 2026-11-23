// Command mediacheck is the acceptance and demonstration adapter for the media
// kernel. It has three subcommands:
//
//	demo    generate fixtures, run remux + RTP + pipeline, print evidence
//	verify  re-probe a source/output pair and prove the round trip
//	rtp     replay the deterministic RTP drill and print gap records
//
// Exit codes: 0 success, 2 corrupt input or failed invariant, 64 usage error,
// 130 cancelled by signal. All temp data stays inside --out.
package main

import (
	"os"

	"github.com/zld/media-check/cmd/mediacheck/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:]))
}
