// Command mediadeliver builds and verifies segment manifests for on-site
// multi-camera deliveries. It is a thin terminal front end: all media
// handling lives in internal/.
package main

import (
	"os"

	"mediadeliver/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
