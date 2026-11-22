//go:build ignore

// Command generate materializes the self-contained fixture set used by the
// demo and by manual inspection. It never reads external media: every byte is
// produced by internal/fixtures. Run with:
//
//	go run ./testdata/generate.go --dir testdata/fixtures
package main

import (
	"flag"
	"fmt"
	"os"

	"mediaarchive/internal/fixtures"
)

func main() {
	dir := flag.String("dir", "testdata/fixtures", "fixture output directory")
	which := flag.String("set", fixtures.SetAll, "fixture set name")
	flag.Parse()
	set, err := fixtures.Generate(*dir, *which)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
	fmt.Printf("fixture_set=%s assets=%d dir=%s\n", *which, len(set.Assets), set.Dir)
	for _, a := range set.Assets {
		fmt.Printf("%s\t%s\tfps=%s\n", a.Name, a.Tag, a.FPS)
	}
}
