package main

import (
	"fmt"
	"io"
	"os"

	"streamcheck/core"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, w io.Writer) int {
	if len(args) == 0 {
		usage(w)
		return core.ExitUsage
	}
	switch args[0] {
	case "demo":
		return demoCmd(args[1:], w)
	case "resume":
		return resumeCmd(args[1:], w)
	default:
		usage(w)
		return core.ExitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `streamcheck — local media stream inspection kernel

usage:
  streamcheck demo   --seed N --fault-set SET --work DIR [--stop-after STAGE]
  streamcheck resume --work DIR

fault-set: all | none | comma list of
  truncated-box,timing-jump,rtp,missing-keyframe,corrupt-nal,migrate-fail
stages: manifest, packet, inspect, migrate
exit codes: 0 ok, 1 result mismatch, 2 usage/input error, 75 checkpoint stop`)
}
