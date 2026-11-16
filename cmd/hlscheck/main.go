// hlscheck slices multi-bitrate MPEG-TS inputs into HLS and verifies
// the published output. Exit codes: 0 clean, 1 data inconsistency,
// 2 argument or I/O error.
package main

import (
	"fmt"
	"os"
)

const (
	exitOK           = 0
	exitInconsistent = 1
	exitUsage        = 2
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	var code int
	var err error
	switch args[0] {
	case "cut":
		code, err = cmdCut(args[1:])
	case "check":
		code, err = cmdCheck(args[1:])
	case "demo":
		code, err = cmdDemo(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hlscheck: unknown command %q\n", args[0])
		usage()
		return exitUsage
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hlscheck:", err)
		return exitUsage
	}
	return code
}

func usage() {
	fmt.Fprint(os.Stderr, `hlscheck - HLS slicing and verification tool

commands:
  cut    --input name=path.ts [--input ...] --out DIR [--target 4.0]
  check  (--root DIR | --dir DIR [--dir ...]) [--json]
  demo   --workdir DIR --profiles 450,900,1800 --segments 12
         [--drop P:N] [--swap P:A,B] [--truncate P:N] [--tamper P:N] [--json]

exit codes: 0 clean, 1 data inconsistency, 2 argument or I/O error
`)
}
