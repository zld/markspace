// Command mediacore is the terminal entry point for the reusable media kernel.
package main

import (
	"os"

	"mediacore/internal/demo"
)

func main() {
	os.Exit(demo.Main(os.Args))
}
