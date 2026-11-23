// Package app implements the mediacheck command handlers. It is separated
// from the command shim so every handler can be tested without process exit.
package app

const (
	ExitOK        = 0
	ExitCorrupt   = 2
	ExitUsage     = 64
	ExitCancelled = 130
)

func Usage() string {
	return `mediacheck - MP4/MOV + RTP integrity kernel

usage:
  mediacheck demo   --seed N --workers W --queue Q --out DIR [--cancel-after MS] [--samples-per N]
  mediacheck verify --input FILE --output FILE
  mediacheck rtp    --window N [--seed N]
`
}
