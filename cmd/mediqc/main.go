// Command mediqc is a terminal-only media QC and re-packaging tool.
//
// Exit codes: 0 = all checks passed, 1 = input or processing failure,
// 2 = bad arguments.
package main

import (
	"fmt"
	"os"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mediqc <command> [flags]\ncommands: demo")
		return 2
	}
	switch args[0] {
	case "demo":
		return demoCmd(args[1:])
	}
	fmt.Fprintln(os.Stderr, "unknown command:", args[0])
	return 2
}
