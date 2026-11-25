package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rtpregress/internal/fixture"
	"rtpregress/internal/media"
	"rtpregress/internal/planner"
	"rtpregress/internal/rtp"
	"rtpregress/internal/timeline"
	"rtpregress/internal/verify"
	"rtpregress/internal/worker"
)

// run executes one command invocation and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	o, err := parseFlags(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitCode(err)
	}
	if o.generate {
		if err := generate(o.out, stdout); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitCode(err)
		}
		return 0
	}
	if err := runRegression(o, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		if loc, ok := media.LocOf(err); ok && (loc.Track != "" || loc.HasOffset) {
			fmt.Fprintf(stderr, "failed at: %s\n", loc.String())
		}
		return exitCode(err)
	}
	return 0
}

func generate(out string, stdout io.Writer) error {
	lay, err := fixture.Generate(out)
	if err != nil {
		return media.At(media.Newf(media.KindInput, "generate fixtures: %v", err),
			media.Loc{Stage: "generate"})
	}
	fmt.Fprintf(stdout, "generated fixtures under %s\n", out)
	fmt.Fprintf(stdout, "  sample    %s\n", lay.SampleMP4)
	fmt.Fprintf(stdout, "  trace     %s\n", lay.Trace)
	fmt.Fprintf(stdout, "  truncated %s\n", lay.Truncated)
	return nil
}

// inputs are the parsed inputs of one run.
type inputs struct {
	mp4Path    string
	tracePath  string
	inputBytes int
	events     []timeline.Event
	reorder    *rtp.ReorderResult
	mvhdScale  uint32
	tracks     []timeline.TrackInfo
	// trace is kept so SEI extraction and timebase errors can name the track.
	trace *rtp.Trace
}

// runRegression loads inputs, plans, executes stages, verifies and prints.
func runRegression(o *options, stdout, stderr io.Writer) error {
	dir := o.input
	if err := validateDir(dir); err != nil {
		return err
	}
	mp4Path, tracePath, err := pickInputs(dir)
	if err != nil {
		return err
	}

	stateDir := o.dir
	if stateDir == "" {
		stateDir = filepath.Join(dir, "run")
	}

	in, err := parseInputs(mp4Path, tracePath, o.window)
	if err != nil {
		// A parse failure aborts before any state is written, so a failed
		// run never leaves a half-valid state directory behind.
		return err
	}

	plan, perr := planFor(in, o)
	if perr != nil {
		return perr
	}
	inputDigest, paramsDigest, err := digests(in, mp4Path, tracePath, plan, o)
	if err != nil {
		return err
	}
	idemKey := o.idemKey
	if idemKey == "" {
		idemKey = strings.TrimPrefix(inputDigest, "sha256:")[:16]
	}

	rc := newRunContext(in, plan, stateDir, mp4Path, tracePath, o)

	fmt.Fprintf(stdout, "sample %s\n", filepath.Base(mp4Path))
	printPlan(stdout, plan)

	runner := worker.NewRunner(worker.Options{
		Dir: stateDir, IdemKey: idemKey,
		InputDigest: inputDigest, ParamsDigest: paramsDigest,
		InterruptAfter: o.interruptAfter,
	}, rc.stages())

	m, rerr := runner.Run(rc.summary())
	if rerr != nil {
		if media.KindOf(rerr) == media.KindInterrupt {
			fmt.Fprintf(stdout, "interrupted after stage %s (state kept in %s)\n",
				interruptedStage(rerr), stateDir)
		}
		return rerr
	}

	// Re-read the delivered artifact and re-run the independent checks even
	// on a completed re-delivery: a file tampered with between runs must not
	// be reported as a success.
	art, err := rc.loadArtifact()
	if err != nil {
		return err
	}
	ad, err := rc.loadAlign()
	if err != nil {
		return err
	}
	res, cerr := verify.Check(art, rc.expected(ad, plan, o))
	if cerr != nil {
		return cerr
	}
	if !res.OK {
		return mismatchError(res.Mismatch)
	}
	artRec := worker.ArtifactRecord{
		Name: filepath.Base(artifactPath(stateDir)), Bytes: art.Size, SHA256: art.FileSHA256,
	}
	summary := rc.summary()
	summary.OutputBytes = art.Size
	summary.OutputSHA256 = art.FileSHA256
	summary.MaxDeviationMs = res.Summary.MaxDeviationMs
	if _, err := runner.MarkCompleted(m, artRec, summary); err != nil {
		return err
	}

	manifestSHA, err := worker.DigestOfFile(runner.ManifestPath())
	if err != nil {
		return media.At(media.Newf(media.KindResume, "digest manifest: %v", err),
			media.Loc{Stage: worker.StageVerify})
	}
	printReport(stdout, rc, ad, art, res, plan, m, manifestSHA, o)
	return nil
}

// pickInputs finds the single MP4 and optional trace in dir.
func pickInputs(dir string) (mp4Path, tracePath string, err error) {
	files, err := filesIn(dir)
	if err != nil {
		return "", "", media.At(media.Newf(media.KindInput, "read input dir: %v", err),
			media.Loc{Stage: "parse"})
	}
	for _, f := range files {
		switch {
		case strings.HasSuffix(f, ".mp4") && mp4Path == "":
			mp4Path = filepath.Join(dir, f)
		case strings.HasSuffix(f, ".trace") && tracePath == "":
			tracePath = filepath.Join(dir, f)
		}
	}
	if mp4Path == "" {
		return "", "", media.At(media.Newf(media.KindInput,
			"input directory %s has no .mp4 file", dir), media.Loc{Stage: "parse"})
	}
	return mp4Path, tracePath, nil
}

// interruptedStage extracts the stage name from an interrupt error.
func interruptedStage(err error) string {
	if loc, ok := media.LocOf(err); ok {
		return loc.Stage
	}
	return "unknown"
}

func parseInputs(mp4Path, tracePath string, window int) (*inputs, error) {
	res, err := timeline.ParseMP4File(mp4Path)
	if err != nil {
		return nil, err
	}
	in := &inputs{mp4Path: mp4Path, tracePath: tracePath}
	in.events = append(in.events, res.Events...)
	in.mvhdScale = res.MVHDTimescale
	in.tracks = res.Tracks

	mp4Info, err := os.Stat(mp4Path)
	if err != nil {
		return nil, media.At(media.Newf(media.KindInput, "stat %s: %v", mp4Path, err),
			media.Loc{Stage: "parse"})
	}
	in.inputBytes = int(mp4Info.Size())

	if tracePath != "" {
		f, err := os.Open(tracePath)
		if err != nil {
			return nil, media.At(media.Newf(media.KindInput, "open trace: %v", err),
				media.Loc{Stage: "parse"})
		}
		tr, err := rtp.ParseTrace(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		re, err := rtp.Reorder(tr, rtp.ReorderOptions{
			Window: uint16(window), Stage: rtp.StageAlign,
		})
		if err != nil {
			return nil, err
		}
		in.reorder = re
		in.trace = tr
		sei, err := timeline.ExtractSEI(tr, re)
		if err != nil {
			return nil, err
		}
		in.events = append(in.events, sei...)
		st, err := os.Stat(tracePath)
		if err != nil {
			return nil, media.At(media.Newf(media.KindInput, "stat trace: %v", err),
				media.Loc{Stage: "parse"})
		}
		in.inputBytes += int(st.Size())
	}
	timeline.SortEvents(in.events)
	return in, nil
}

func planFor(in *inputs, o *options) (*planner.Plan, error) {
	textBytes := 0
	for _, e := range in.events {
		textBytes += len(e.Text)
	}
	prob := planner.Problem{
		Events:     len(in.events),
		TextBytes:  textBytes,
		DurationNs: timeline.DurationOf(in.events),
	}
	plan, err := planner.Choose(prob, planner.Constraints{
		BudgetMS: o.budgetMS, MaxBytes: o.maxBytes, QualityMin: o.qualityMin,
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func printPlan(w io.Writer, p *planner.Plan) {
	fmt.Fprintf(w, "plan %s quality=%.4f cost=%.1fms segments=%d bytes=%d\n",
		p.Candidate.String(), p.Quality, p.CostMS, p.Segments, p.EstimatedBytes)
}

func digests(in *inputs, mp4Path, tracePath string, plan *planner.Plan, o *options) (string, string, error) {
	files := map[string]string{}
	h, err := worker.DigestOfFile(mp4Path)
	if err != nil {
		return "", "", media.At(media.Newf(media.KindInput, "digest input: %v", err),
			media.Loc{Stage: "parse"})
	}
	files[filepath.Base(mp4Path)] = h
	if tracePath != "" {
		h, err := worker.DigestOfFile(tracePath)
		if err != nil {
			return "", "", media.At(media.Newf(media.KindInput, "digest trace: %v", err),
				media.Loc{Stage: "parse"})
		}
		files[filepath.Base(tracePath)] = h
	}
	params := worker.ParamsDigest(map[string]string{
		"budgetMs":    fmt.Sprintf("%.1f", o.budgetMS),
		"maxBytes":    fmt.Sprintf("%d", o.maxBytes),
		"qualityMin":  fmt.Sprintf("%.3f", o.qualityMin),
		"segmentMs":   fmt.Sprintf("%d", plan.SegmentMS),
		"precisionUs": fmt.Sprintf("%d", plan.PrecisionUS),
		"bitrateKbps": fmt.Sprintf("%d", plan.BitrateKbps),
		"window":      fmt.Sprintf("%d", o.window),
	})
	return worker.InputDigest(files), params, nil
}

// printReport writes the item-by-item input-to-output map and digest block.
func printReport(w io.Writer, rc *runContext, ad *alignDoc, art *verify.Artifact,
	res *verify.Result, plan *planner.Plan, m *worker.Manifest, manifestSHA string, o *options) {

	for _, e := range ad.Events {
		fmt.Fprintf(w, "%s\n", e.Line(""))
	}
	fmt.Fprintf(w, "sample=%s output=%s\n",
		filepath.Base(rc.mp4Path), filepath.Base(artifactPath(rc.dir)))
	fmt.Fprintf(w, "events=%d", len(ad.Events))
	counts := map[string]int{}
	for _, e := range ad.Events {
		counts[string(e.Source)]++
	}
	for _, src := range sortedKeys(counts) {
		fmt.Fprintf(w, " %s=%d", src, counts[src])
	}
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "max-deviation=%.3fms limit=%.3fms\n",
		res.Summary.MaxDeviationMs, float64(plan.QuantizeDeviationNs())/1e6)
	fmt.Fprintf(w, "input-bytes=%d output-bytes=%d max-bytes=%d\n",
		rc.in.inputBytes, art.Size, o.maxBytes)
	fmt.Fprintf(w, "quality=%.4f quality-min=%.4f\n", plan.Quality, o.qualityMin)
	fmt.Fprintf(w, "failure-stage=none offset=none\n")
	if re := rc.in.reorder; re != nil {
		fmt.Fprintf(w, "rtp committed=%d loss=%d duplicates=%d late=%d\n",
			len(re.Packets), re.PermanentlyLost(), re.Duplicates, len(re.Late))
		for _, late := range re.Late {
			fmt.Fprintf(w, "late packet seq=%d first-stage=%s position=%d duplicate=%t\n",
				late.SequenceNumber, late.FirstStage, late.Position, late.Duplicate)
		}
	}
	fmt.Fprintf(w, "artifact %s %d bytes body %s\n", art.FileSHA256, art.Size, art.BodySHA256)
	fmt.Fprintf(w, "manifest %s sha256=%s\n", m.IdemKey, manifestSHA)
	for _, st := range m.Stages {
		fmt.Fprintf(w, "stage %s\n", st.Describe())
	}
}

func formatRecord(r verify.Record) string {
	return fmt.Sprintf("t=%.3fms seg=%d track=%s src=%s bytes=%d text=%s",
		float64(r.TimeNs)/1e6, r.Segment, r.Track, r.Source, len(r.Text), sanitizeText(r.Text))
}

func sanitizeText(s string) string {
	r := strings.NewReplacer("\n", "\\n", "\r", "\\r", "\t", "\\t")
	return r.Replace(s)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
