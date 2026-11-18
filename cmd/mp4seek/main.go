// mp4seek builds and queries random-access frame indexes for MP4 files.
package main

import (
	"fmt"
	"os"
)

// Exit codes: 0 success, 2 argument error, 3 corrupt file/container,
// 4 time not locatable.
const (
	exitOK       = 0
	exitArgs     = 2
	exitCorrupt  = 3
	exitNotFound = 4
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mp4seek <fixture|inspect|locate|export> [flags]")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitArgs)
	}
	var code int
	switch os.Args[1] {
	case "fixture":
		code = cmdFixture(os.Args[2:])
	case "inspect":
		code = cmdInspect(os.Args[2:])
	case "locate":
		code = cmdLocate(os.Args[2:])
	case "export":
		code = cmdExport(os.Args[2:])
	default:
		usage()
		code = exitArgs
	}
	os.Exit(code)
}
