package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"media-archiver/internal/media"
	"media-archiver/internal/samplegen"
)

func main() {
	out := flag.String("out", filepath.Join("testdata", "fixtures"), "fixture output root")
	seed := flag.Int64("seed", 17, "deterministic seed")
	flag.Parse()

	samples := []struct {
		name      string
		injection string
	}{
		{name: "normal", injection: "none"},
		{name: "truncated-wav", injection: "truncate"},
		{name: "backward-timestamps", injection: "backward"},
		{name: "duplicate-block", injection: "duplicate"},
		{name: "corrupt-index", injection: "none"},
	}
	for _, s := range samples {
		dir := filepath.Join(*out, s.name)
		path := filepath.Join(dir, "sample.wav")
		gen, err := samplegen.Generate(samplegen.Options{Seed: *seed, Injection: s.injection, OutputPath: path})
		if err != nil {
			fatalf("generate %s: %v", s.name, err)
		}
		expected := gen.Expected
		if s.injection == "none" {
			expected.TargetFrame = 6*samplegen.DemoBlockFrames + 64
			expected.ExpectedBlock = 6
			expected.ExpectedFrameOffset = 64
		}
		data, err := media.MarshalCanonicalJSON(expected)
		if err != nil {
			fatalf("marshal expected %s: %v", s.name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "expected.json"), data, 0o644); err != nil {
			fatalf("write expected %s: %v", s.name, err)
		}
		if s.name == "corrupt-index" {
			corrupt := []byte(`{"version":1,"entries":[`)
			if err := os.WriteFile(filepath.Join(dir, "corrupt-index.json"), corrupt, 0o644); err != nil {
				fatalf("write corrupt index fixture: %v", err)
			}
		}
		fmt.Printf("generated %s\n", dir)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
