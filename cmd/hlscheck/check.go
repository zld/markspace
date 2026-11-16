package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"hlscheck/internal/check"
	"hlscheck/internal/report"
)

func cmdCheck(args []string) (int, error) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	root := fs.String("root", "", "directory containing profile subdirectories")
	jsonOut := fs.Bool("json", false, "emit JSON summary")
	var dirs strList
	fs.Var(&dirs, "dir", "profile directory, repeatable")
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	if *root != "" {
		entries, err := os.ReadDir(*root)
		if err != nil {
			return exitUsage, err
		}
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(*root, e.Name()))
			}
		}
	}
	if len(dirs) == 0 {
		return exitUsage, fmt.Errorf("check: --root or at least one --dir is required")
	}
	sort.Strings(dirs)
	sum := runCheck(dirs)
	if *jsonOut {
		if err := report.JSON(os.Stdout, sum); err != nil {
			return exitUsage, err
		}
	} else {
		report.Text(os.Stdout, sum)
	}
	if !sum.OK() {
		return exitInconsistent, nil
	}
	return exitOK, nil
}

// runCheck verifies every profile directory and cross-profile alignment.
func runCheck(dirs []string) *report.Summary {
	sum := &report.Summary{}
	for _, d := range dirs {
		sum.Profiles = append(sum.Profiles, check.CheckProfile(d))
	}
	if len(dirs) > 1 {
		sum.Cross = check.CheckCross(sum.Profiles...)
	}
	return sum
}
