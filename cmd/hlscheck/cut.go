package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

// strList collects repeated string flags.
type strList []string

func (s *strList) String() string { return fmt.Sprint([]string(*s)) }
func (s *strList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func cmdCut(args []string) (int, error) {
	fs := flag.NewFlagSet("cut", flag.ContinueOnError)
	out := fs.String("out", "", "output root directory (required)")
	target := fs.Float64("target", 4.0, "target segment duration in seconds")
	var inputs strList
	fs.Var(&inputs, "input", "name=path.ts, repeatable")
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	if *out == "" || len(inputs) == 0 {
		return exitUsage, fmt.Errorf("cut: --out and at least one --input are required")
	}
	var jobs []job
	for _, in := range inputs {
		name, path, ok := strings.Cut(in, "=")
		if !ok || name == "" || path == "" {
			return exitUsage, fmt.Errorf("cut: bad --input %q, want name=path.ts", in)
		}
		jobs = append(jobs, job{name: name, input: path})
	}
	if err := sliceAll(context.Background(), jobs, *out, *target); err != nil {
		return exitUsage, err
	}
	fmt.Printf("cut: published %d profile(s) under %s\n", len(jobs), *out)
	return exitOK, nil
}
