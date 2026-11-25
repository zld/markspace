package main

import "os"

func main() { os.Exit(run(nil, os.Stdout, os.Stderr)) }

func run(_ []string, stdout interface{ Write([]byte) (int, error) }, _ interface{ Write([]byte) (int, error) }) int {
	_, _ = stdout.Write([]byte("archivecheck scaffold\n"))
	return 0
}
