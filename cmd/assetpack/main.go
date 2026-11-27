// Command assetpack normalizes a release's audio and commits a verified,
// versioned manifest.  It is a thin terminal adapter: all behaviour lives in
// the internal packages.
package main

import (
	"os"

	"example.com/assetpack/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
