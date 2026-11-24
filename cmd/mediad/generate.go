package main

import (
	"os"

	"mediad/internal/fixture"
)

// cmdGenerate writes a self-contained demo or corrupt stream.
func cmdGenerate(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if err := f.require("--out"); err != nil {
		return err
	}
	kind := "demo"
	// The acceptance syntax passes demo|corrupt as a positional token.
	for _, a := range f.positional() {
		if a == "demo" || a == "corrupt" {
			kind = a
		}
	}

	var stream []byte
	switch kind {
	case "demo":
		stream, _, _ = fixture.Build(fixture.Default())
	case "corrupt":
		stream, _ = fixture.Corrupt()
	default:
		return badArgs("unknown --generate kind %q (want demo|corrupt)", kind)
	}
	return os.WriteFile(f.get("--out", ""), stream, 0o644)
}

// cmdInspect parses a stream and prints the first corruption location in the
// exact error_code/offset/stage shape, or OK statistics when clean.
func cmdInspect(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if err := f.require("--in"); err != nil {
		return err
	}
	raw, err := os.ReadFile(f.get("--in", ""))
	if err != nil {
		return err
	}
	count, err := countFrames(raw)
	if err != nil {
		return err // ParseError is mapped to exit code 2 by main
	}
	outf("status=OK frames=%d", count)
	return nil
}
