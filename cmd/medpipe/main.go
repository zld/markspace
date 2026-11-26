// Command medpipe is the pure-local terminal adapter for the media kernel.
package main

import "os"

func main() {
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}
