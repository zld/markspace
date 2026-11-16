package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"hlscheck/internal/check"
	"hlscheck/internal/demo"
	"hlscheck/internal/report"
)

// cmdDemo generates deterministic fixtures, slices them, injects the
// requested faults and verifies that the checker reports them exactly.
// It exits 0 when every injected fault is accurately reported.
func cmdDemo(args []string) (int, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	workdir := fs.String("workdir", "/tmp/hlscheck", "scratch directory")
	profiles := fs.String("profiles", "450,900,1800", "comma-separated bitrates in kbps")
	segments := fs.Int("segments", 12, "segments per profile")
	target := fs.Float64("target", 4.0, "target segment duration in seconds")
	jsonOut := fs.Bool("json", false, "emit JSON summary")
	var faults []demo.Fault
	fs.Var(demo.FaultFlag{List: &faults, Kind: "drop"}, "drop", "inject missing segment: profile:seq")
	fs.Var(demo.FaultFlag{List: &faults, Kind: "swap"}, "swap", "inject swapped segments: profile:a,b")
	fs.Var(demo.FaultFlag{List: &faults, Kind: "truncate"}, "truncate", "inject truncated segment: profile:seq")
	fs.Var(demo.FaultFlag{List: &faults, Kind: "tamper"}, "tamper", "inject bit corruption: profile:seq")
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	rates, err := parseProfiles(*profiles)
	if err != nil {
		return exitUsage, err
	}
	inDir := filepath.Join(*workdir, "input")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		return exitUsage, err
	}
	var jobs []job
	for _, rate := range rates {
		path := filepath.Join(inDir, fmt.Sprintf("%d.ts", rate))
		if err := genFixture(path, rate, *segments, *target); err != nil {
			return exitUsage, err
		}
		jobs = append(jobs, job{name: strconv.Itoa(rate), input: path})
	}
	root := filepath.Join(*workdir, "hls")
	if err := sliceAll(context.Background(), jobs, root, *target); err != nil {
		return exitUsage, err
	}
	for _, fl := range faults {
		if err := demo.Apply(root, fl); err != nil {
			return exitUsage, err
		}
	}
	var dirs []string
	for _, j := range jobs {
		dirs = append(dirs, filepath.Join(root, j.name))
	}
	sum := runCheck(dirs)
	if *jsonOut {
		if err := report.JSON(os.Stdout, sum); err != nil {
			return exitUsage, err
		}
	} else {
		report.Text(os.Stdout, sum)
	}
	miss := verifyFaults(faults, sum)
	for _, m := range miss {
		fmt.Fprintln(os.Stderr, "demo: fault reporting mismatch:", m)
	}
	if len(miss) > 0 {
		return exitInconsistent, nil
	}
	fmt.Printf("demo: all %d injected fault(s) accurately reported\n", len(faults))
	return exitOK, nil
}

func genFixture(path string, rate, segments int, target float64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = demo.Generate(f, demo.GenOptions{
		BitrateKbps: rate,
		DurationSec: float64(segments) * target,
		GOPSec:      2,
		Seed:        int64(rate),
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func parseProfiles(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("bad profile bitrate %q", p)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no profiles given")
	}
	return out, nil
}

// verifyFaults checks that every injected fault was reported and that
// untouched profiles stayed clean.
func verifyFaults(faults []demo.Fault, sum *report.Summary) []string {
	var miss []string
	byRate := map[int]*check.ProfileReport{}
	for _, p := range sum.Profiles {
		byRate[p.BitrateKbps] = p
	}
	faulted := map[int]bool{}
	for _, fl := range faults {
		faulted[fl.Profile] = true
		rep := byRate[fl.Profile]
		if rep == nil {
			miss = append(miss, fmt.Sprintf("profile %d not checked", fl.Profile))
			continue
		}
		switch fl.Kind {
		case "drop":
			if !containsInt(rep.Missing, fl.A) {
				miss = append(miss, fmt.Sprintf("drop %d:%d not reported as missing", fl.Profile, fl.A))
			}
		case "swap":
			if rep.Reordered < 1 {
				miss = append(miss, fmt.Sprintf("swap %d:%d,%d not reported as reordered", fl.Profile, fl.A, fl.B))
			}
		case "truncate", "tamper":
			if !containsInt(rep.ChecksumFailures, fl.A) {
				miss = append(miss, fmt.Sprintf("%s %d:%d not reported as checksum failure", fl.Kind, fl.Profile, fl.A))
			}
		}
	}
	for _, p := range sum.Profiles {
		if !faulted[p.BitrateKbps] && !p.OK() {
			miss = append(miss, fmt.Sprintf("clean profile %d reported faults", p.BitrateKbps))
		}
	}
	return miss
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
