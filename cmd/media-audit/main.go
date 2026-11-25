package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"media-audit/internal/manifest"
	"media-audit/internal/pipeline"
	"media-audit/internal/selftest"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("media-audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	selfTest := fs.Bool("self-test", false, "generate fixtures and verify all audit behaviors")
	out := fs.String("out", "", "manifest JSON output path")
	fixtureDir := fs.String("fixtures-dir", selftest.DefaultFixtureDir(), "generated fixture directory")
	stateDir := fs.String("state-dir", selftest.DefaultStateDir(), "persistent task/checkpoint/index directory")
	input := fs.String("input", "", "input media path, or comma-separated paths for one logical task")
	taskKey := fs.String("task-key", "", "stable task key (defaults to a hash of input paths)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	if *selfTest {
		if strings.TrimSpace(*out) == "" {
			fmt.Fprintln(stderr, "--out is required with --self-test")
			return 2
		}
		return runSelfTest(*fixtureDir, *stateDir, *out, stdout, stderr)
	}

	if strings.TrimSpace(*input) == "" {
		fmt.Fprintln(stderr, "usage: media-audit --self-test --out manifest.json")
		fmt.Fprintln(stderr, "       media-audit --input file.mxf[,file.mp4] [--out manifest.json]")
		return 2
	}
	paths := strings.Split(*input, ",")
	for i := range paths {
		paths[i] = strings.TrimSpace(paths[i])
		if _, err := os.Stat(paths[i]); err != nil {
			fmt.Fprintf(stderr, "invalid --input %q: %v\n", paths[i], err)
			return 2
		}
	}
	key := *taskKey
	if key == "" {
		abs := make([]string, 0, len(paths))
		for _, p := range paths {
			a, _ := filepath.Abs(p)
			abs = append(abs, a)
		}
		key = manifest.TaskKey("input", strings.Join(abs, "|"))
	}

	p, err := pipeline.New(*stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline init failed: %v\n", err)
		return 1
	}
	defer p.Close()
	item, err := p.ProcessPaths(key, filepath.Base(paths[0]), "input", pipeline.Expectation{}, paths)
	if err != nil {
		fmt.Fprintf(stderr, "processing failed: %v\n", err)
		return 1
	}
	mr := manifest.NewMerger()
	mr.Submit(item)
	m := manifest.BuildManifest(mr.Items(), nil)
	if *out != "" {
		if err := manifest.WriteJSON(*out, m); err != nil {
			fmt.Fprintf(stderr, "write manifest: %v\n", err)
			return 1
		}
	} else {
		b, _ := json.MarshalIndent(m, "", "  ")
		fmt.Fprintln(stdout, string(b))
	}
	fmt.Fprintf(stdout, "digest=%s items=%d errors=%d\n", m.Digest, m.Summary.ItemCount, m.Summary.ErrorCount)
	return 0
}

func runSelfTest(fixtureDir, stateDir, out string, stdout, stderr io.Writer) int {
	result, err := selftest.Run(fixtureDir, stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "self-test processing failed: %v\n", err)
		return 1
	}
	if werr := manifest.WriteJSON(out, result.Manifest); werr != nil {
		fmt.Fprintf(stderr, "write manifest %s: %v\n", out, werr)
		return 1
	}

	m := result.Manifest
	fmt.Fprintf(stdout, "media-audit self-test: samples=%d items=%d normal=%d error_items=%d errors=%d digest=%s\n",
		m.Summary.SampleCount, m.Summary.ItemCount, m.Summary.NormalItemCount,
		m.Summary.ErrorItemCount, m.Summary.ErrorCount, m.Digest)
	for _, mp := range m.SampleMappings {
		item := findItem(m, mp.StableKey)
		status := "ok"
		offset := int64(-1)
		stage := ""
		if item != nil {
			if item.ErrorCount > 0 {
				status = fmt.Sprintf("%d errors", item.ErrorCount)
			}
			if len(item.Errors) > 0 {
				offset = item.Errors[0].Offset
			}
			if item.Recovery != nil {
				stage = item.Recovery.Stage
			}
		}
		fmt.Fprintf(stdout, "  %-20s -> %s/%s status=%s first_offset=%d expected=%d recovery=%s\n",
			mp.SampleID, mp.StableKey, mp.OutputItemID, status, offset, mp.ExpectedDamageOffset, stage)
	}
	fmt.Fprintf(stdout, "manifest=%s\n", out)

	if len(result.Failures) > 0 {
		fmt.Fprintln(stderr, "SELF-TEST FAILURES:")
		for _, f := range result.Failures {
			fmt.Fprintln(stderr, " -", f)
		}
		return 1
	}
	fmt.Fprintln(stdout, "SELF-TEST PASS")
	return 0
}

func findItem(m *manifest.Manifest, key string) *manifest.Item {
	for _, it := range m.Items {
		if it.StableKey == key {
			return it
		}
	}
	return nil
}
