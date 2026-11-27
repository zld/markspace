// Command continuity drives the reusable media-continuity kernel from
// the terminal: it generates sample inputs, probes, plans, cuts,
// manifests and verifies them, printing acceptance metrics as JSON.
//
// Exit codes: 0 success; 1 parameter or budget error; 2 corrupt
// container (truncated box); 3 keyframe miss; 4 verification failure.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"continuity/fixture"
	"continuity/manifest"
	"continuity/media"
	"continuity/planner"
	"continuity/segment"
	"continuity/verify"
	"continuity/worker"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type output struct {
	Case                   string   `json:"case"`
	RoundtripRate          float64  `json:"roundtrip_rate"`
	KeyframeHitRate        float64  `json:"keyframe_hit_rate"`
	SegmentHashes          []string `json:"segment_hashes"`
	AlignedBoundaryDeltaNs int64    `json:"aligned_boundary_delta_ns"`
	Verified               bool     `json:"verified"`
	ManifestHash           string   `json:"manifest_hash"`
	ReusedSegments         int      `json:"reused_segments,omitempty"`
	RecomputedSegments     int      `json:"recomputed_segments,omitempty"`
	ManifestStable         *bool    `json:"manifest_stable,omitempty"`
	WorkDir                string   `json:"workdir"`
}

type errOutput struct {
	Kind   string `json:"kind"`
	Error  string `json:"error"`
	Offset *int64 `json:"offset,omitempty"`
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "demo" {
		fmt.Fprintln(stderr, "usage: continuity demo --case <case> [--representations N] [--segment 2s]")
		return 1
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	caseName := fs.String("case", "valid", "valid|corrupt-box|missing-keyframe|resume|audio-conflict|color-conflict")
	reps := fs.Int("representations", 2, "number of representation inputs (1..8)")
	segDur := fs.Duration("segment", 2*time.Second, "target segment duration")
	workdir := fs.String("workdir", "", "output directory (default: temp dir)")
	maxBuf := fs.Int64("max-buffer", 16<<20, "transient buffer budget in bytes")
	maxConc := fs.Int("max-concurrency", 4, "worker pool size (1..64)")
	tCh := fs.Int("target-channels", 0, "explicit target channel layout (0 = must agree)")
	tRate := fs.Int("target-sample-rate", 0, "explicit target sample rate (0 = must agree)")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if *reps < 1 || *reps > 8 || *segDur <= 0 || *maxConc < 1 || *maxConc > 64 || *maxBuf <= 0 {
		fmt.Fprintln(stderr, "invalid parameters: check --representations/--segment/--max-concurrency/--max-buffer")
		return 1
	}

	dir := *workdir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "continuity-")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	inDir := filepath.Join(dir, "inputs")
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	specs, err := caseSpecs(*caseName, *reps)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	inputs, code := generateAndProbe(specs, inDir, stderr)
	if code != 0 {
		return code
	}

	constraints := planner.Constraints{
		MaxBufferBytes: *maxBuf, MaxConcurrency: *maxConc,
		TargetChannels: uint16(*tCh), TargetSampleRate: uint32(*tRate),
	}
	if *caseName == "color-conflict" {
		// The demo resolves the conflict with an explicit selection.
		sel := media.ColorMeta{Present: true, Primaries: 1, Transfer: 1, Matrix: 1}
		constraints.ColorOutput = &sel
	}

	plan, rejections, err := planner.Evaluate(inputs, segDur.Nanoseconds(), constraints)
	if err != nil {
		for _, r := range rejections {
			fmt.Fprintf(stderr, "rejected candidate %d ns: %s\n", r.SegmentNanos, r.Reason)
		}
		return fail(stderr, err)
	}

	pool := &worker.Pool{Size: *maxConc}
	if *caseName == "resume" {
		return runResume(inputs, plan, outDir, dir, pool, stdout, stderr)
	}

	res, err := segment.Cut(context.Background(), inputs, plan, outDir, pool)
	if err != nil {
		return fail(stderr, err)
	}
	out, code := finish(*caseName, inputs, plan, res, outDir, dir, stderr)
	if code != 0 {
		return code
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
	return 0
}

// generateAndProbe writes the fixture inputs and probes them.
func generateAndProbe(specs []fixture.Spec, inDir string, stderr io.Writer) ([]*media.Container, int) {
	var inputs []*media.Container
	for i, spec := range specs {
		path := filepath.Join(inDir, fmt.Sprintf("rep%d.mp4", i))
		if err := fixture.Generate(path, spec); err != nil {
			fmt.Fprintln(stderr, err)
			return nil, 1
		}
		c, err := media.Probe(path)
		if err != nil {
			fail(stderr, err)
			return nil, exitCode(err)
		}
		inputs = append(inputs, c)
	}
	return inputs, 0
}

// finish builds the manifest, verifies the output and returns the
// metrics document. On verification failure it prints the report to
// stderr and returns a non-zero code.
func finish(caseName string, inputs []*media.Container, plan *planner.Plan,
	res *segment.Result, outDir, workdir string, stderr io.Writer) (output, int) {

	m := manifest.Build(inputs, plan, res)
	if err := manifest.Write(outDir, m); err != nil {
		fmt.Fprintln(stderr, err)
		return output{}, 1
	}
	rep, err := verify.Check(outDir, m, inputs, plan, res.HitRate)
	if err != nil {
		enc := json.NewEncoder(stderr)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
		return output{}, 4
	}
	mh, err := manifest.Hash(m)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return output{}, 1
	}
	out := output{
		Case: caseName, RoundtripRate: rep.RoundtripRate,
		KeyframeHitRate: rep.KeyframeHitRate,
		AlignedBoundaryDeltaNs: rep.AlignedBoundaryDeltaNs,
		Verified: rep.Verified, ManifestHash: mh, WorkDir: workdir,
		ReusedSegments: res.Reused, RecomputedSegments: res.Recomputed,
	}
	for _, s := range res.Segments {
		out.SegmentHashes = append(out.SegmentHashes, s.SHA256)
	}
	return out, 0
}

// fail prints the error as JSON and returns its exit code.
func fail(stderr io.Writer, err error) int {
	eo := errOutput{Kind: "internal", Error: err.Error()}
	if me, ok := err.(*media.Error); ok {
		eo.Kind = me.Kind.String()
		if me.HasOffset {
			off := me.Offset
			eo.Offset = &off
		}
	}
	enc := json.NewEncoder(stderr)
	enc.SetIndent("", "  ")
	enc.Encode(eo)
	return exitCode(err)
}

// exitCode maps the error kind to the process exit code.
func exitCode(err error) int {
	switch {
	case media.IsKind(err, media.KindTruncatedBox), media.IsKind(err, media.KindBadTimescale),
		media.IsKind(err, media.KindTimeRegression), media.IsKind(err, media.KindOverflow):
		return 2
	case media.IsKind(err, media.KindKeyframeMiss):
		return 3
	case media.IsKind(err, media.KindVerify):
		return 4
	default:
		return 1
	}
}
