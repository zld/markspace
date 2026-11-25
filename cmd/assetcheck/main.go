// Command assetcheck is the terminal entry point for offline media delivery
// validation and manifest migration. It contains no processing logic itself:
// every command calls the reusable pipeline and prints quantitative results.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"example.com/assetcheck/internal/manifest"
	"example.com/assetcheck/internal/pipeline"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/testdata"
)

// Exit codes are the documented CLI contract.
const (
	exitOK         = timeline.ExitOK
	exitBadInput   = timeline.ExitBadInput
	exitCheckpoint = timeline.ExitCheckpoint
	exitLocalIO    = timeline.ExitLocalIO
	exitUsage      = 64
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "demo":
		err = runDemo(rest, stdout, stderr)
	case "verify":
		err = runVerify(rest, stdout, stderr)
	case "migrate":
		err = runMigrate(rest, stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", cmd)
		usage(stderr)
		return exitUsage
	}
	if err != nil {
		// A command that already printed a full failure report marks it so the
		// terminal does not show the same diagnosis twice.
		var reported *reportedError
		if !errors.As(err, &reported) {
			printError(stderr, err)
		}
		return timeline.ExitCodeOf(err)
	}
	return exitOK
}

// reportedError marks an error whose structured report was already written.
type reportedError struct{ err error }

func (r *reportedError) Error() string { return r.err.Error() }
func (r *reportedError) Unwrap() error { return r.err }
func (r *reportedError) ExitCode() int { return timeline.ExitCodeOf(r.err) }
func (r *reportedError) As(target interface{}) bool {
	if te, ok := target.(**timeline.Error); ok {
		return errors.As(r.err, te)
	}
	return false
}

func usage(w io.Writer) {
	fmt.Fprint(w, `assetcheck - offline Matroska/WebM delivery validation and manifest migration

Usage:
  assetcheck demo    --seed N --out DIR
  assetcheck verify  --input FILE [--manifest FILE] [--rtp FILE]
  assetcheck migrate --input FILE --manifest FILE --out DIR
                     [--stop-after parse|normalize|write] [--resume]

Exit codes:
  0  success
  2  input corrupt or specification violation
  3  checkpoint mismatch
  4  local read/write failure
`)
}

func printError(w io.Writer, err error) {
	var te *timeline.Error
	if errors.As(err, &te) {
		fmt.Fprintf(w, "error: %s\n", te.Error())
		fmt.Fprintf(w, "exit: %d\n", te.ExitCode())
		return
	}
	fmt.Fprintf(w, "error: %v\n", err)
	fmt.Fprintf(w, "exit: %d\n", timeline.ExitCodeOf(err))
}

// --- demo ------------------------------------------------------------------

func runDemo(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "fixture seed; the same seed writes identical bytes")
	out := fs.String("out", "", "output directory for generated fixtures")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if *out == "" {
		return usageErr(errors.New("demo requires --out"))
	}
	start := time.Now()
	res, err := testdata.Generate(*out, *seed)
	if err != nil {
		return ioErr(err, "generate fixtures")
	}
	files := sortedFiles(res)
	fmt.Fprintf(stdout, "generated %d files in %s (seed %d, %d ms)\n",
		len(files), *out, *seed, time.Since(start).Milliseconds())
	for _, f := range files {
		fmt.Fprintf(stdout, "  %-22s %d bytes\n", f.name, f.size)
	}
	fmt.Fprintln(stdout, "\npreset fault offsets:")
	if len(res.Expectations) == 0 {
		fmt.Fprintln(stdout, "  (none)")
	}
	for _, e := range res.Expectations {
		fmt.Fprintf(stdout, "  %-22s category=%-24s element=0x%-6X offset=%d\n",
			e.File, e.Category, e.ElementID, e.Offset)
	}
	fmt.Fprintln(stdout, "\nmanifests:")
	fmt.Fprintf(stdout, "  %-22s legacy schema v1\n", testdata.ManifestV1)
	fmt.Fprintf(stdout, "  %-22s current schema v2\n", testdata.ManifestV2)
	return nil
}

type fileInfo struct {
	name string
	size int64
}

func sortedFiles(res *testdata.Result) []fileInfo {
	entries, err := os.ReadDir(res.Dir)
	if err != nil {
		return nil
	}
	out := make([]fileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		out = append(out, fileInfo{name: e.Name(), size: info.Size()})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].name < out[j-1].name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// --- verify ----------------------------------------------------------------

func runVerify(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "media file to verify")
	manifestPath := fs.String("manifest", "", "optional delivery manifest")
	rtpPath := fs.String("rtp", "", "optional RTP capture dump")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if *input == "" {
		return usageErr(errors.New("verify requires --input"))
	}
	opts := pipeline.VerifyOptions{RTPDump: *rtpPath}
	if *manifestPath != "" {
		m, err := loadManifestProfile(*manifestPath)
		if err != nil {
			return err
		}
		opts.Profile = m
	}
	rep, err := pipeline.Verify(*input, opts)
	if err != nil {
		if rep == nil {
			printStructuredError(stdout, err)
		} else {
			printVerify(stdout, rep, err)
		}
		printPresetAudit(stdout, *input, err)
		return &reportedError{err: err}
	}
	printVerify(stdout, rep, nil)
	return nil
}

// printStructuredError prints the full structured error on stdout so a report
// and its diagnosis stay in one stream in a fixed order.
func printStructuredError(w io.Writer, err error) {
	var te *timeline.Error
	if errors.As(err, &te) {
		fmt.Fprintf(w, "failure:    stage=%s category=%s element=0x%X offset=%d tracks=%v reason=%s\n",
			te.Stage, te.Category, te.ElementID, te.Offset, te.Tracks, te.Reason)
		return
	}
	fmt.Fprintf(w, "failure:    %v\n", err)
}

// printPresetAudit compares a reported failure offset with the offset the
// fixture generator preset for this file. A correct run shows a 0-byte delta.
func printPresetAudit(w io.Writer, input string, verifyErr error) {
	dir := filepath.Dir(input)
	base := filepath.Base(input)
	expectations, err := testdata.LoadExpectations(dir)
	if err != nil {
		return
	}
	for _, e := range expectations {
		if e.File != base {
			continue
		}
		reported := timeline.OffsetOf(verifyErr)
		cat := timeline.CategoryOf(verifyErr)
		delta := reported - e.Offset
		fmt.Fprintf(w, "preset:     category=%s element=0x%X offset=%d stage=%s\n",
			e.Category, e.ElementID, e.Offset, e.Stage)
		fmt.Fprintf(w, "reported:   category=%s element=0x%X offset=%d\n",
			cat, elementIDOf(verifyErr), reported)
		fmt.Fprintf(w, "offset-delta: %d bytes\n", delta)
		fmt.Fprintf(w, "audit:      %s\n", auditVerdict(cat, e.Category, delta))
		return
	}
}

func elementIDOf(err error) uint32 {
	var te *timeline.Error
	if errors.As(err, &te) {
		return te.ElementID
	}
	return 0
}

func auditVerdict(got timeline.Category, want string, delta int64) string {
	if string(got) != want {
		return fmt.Sprintf("MISMATCH (category %s, preset %s)", got, want)
	}
	if delta != 0 {
		return "MISMATCH (offset differs from the generator preset)"
	}
	return "OK (offset equals the generator preset)"
}

func loadManifestProfile(path string) (manifest.Profile, error) {
	m, err := manifest.Load(path)
	if err != nil {
		return manifest.Profile{}, err
	}
	if err := m.ValidateCrossFields(); err != nil {
		return manifest.Profile{}, err
	}
	return m.Profile(), nil
}

func printVerify(w io.Writer, rep *pipeline.VerifyReport, verifyErr error) {
	fmt.Fprintf(w, "input:      %s\n", rep.Asset.Path)
	fmt.Fprintf(w, "container:  %s (%s v%d)\n", rep.Asset.Container, rep.Asset.DocType, rep.Asset.Version)
	fmt.Fprintf(w, "elements:   %d (%s)\n", rep.ElementCount, pipeline.ElementSummary(rep.Asset))
	fmt.Fprintf(w, "tracks:     %d\n", rep.TrackCount)
	for _, t := range rep.Tracks {
		fmt.Fprintf(w, "  track %d  %-8s %-18s lang=%s name=%s offset=%d\n",
			t.Number, t.Type.Name(), t.CodecID, t.Language, orDash(t.Name), t.Offset)
	}
	fmt.Fprintf(w, "fragments:  %d in %d clusters\n", rep.FragmentCount, rep.Fragments.ClusterCount)
	fmt.Fprintf(w, "max-cross-track-deviation: %s (%d ns) at offset %d\n",
		timeline.FormatNS(rep.MaxDeviation), int64(rep.MaxDeviation), rep.DeviationAt)
	fmt.Fprintf(w, "sha256:     %s\n", rep.SHA256)
	if rep.RTP != nil {
		fmt.Fprintf(w, "rtp:        %d packets in %d streams, max alignment skew %d ns\n",
			rep.RTP.Packets, rep.RTP.Streams, int64(rep.RTP.MaxSkew))
	}
	if verifyErr == nil {
		fmt.Fprintln(w, "result:     OK")
		return
	}
	printIssues(w, rep)
	fmt.Fprintln(w, "result:     FAILED")
}

func printIssues(w io.Writer, rep *pipeline.VerifyReport) {
	for _, is := range rep.Issues {
		fmt.Fprintf(w, "issue: category=%s offset=%d tracks=%v field=%s reason=%s\n",
			is.Category, is.Offset, is.Tracks, is.Field, is.Reason)
	}
	for _, p := range rep.Fragments.Problems {
		fmt.Fprintf(w, "issue: category=%s offset=%d tracks=%v reason=%s\n",
			p.Category, p.Offset, p.Tracks, p.Reason)
	}
	if rep.RTP != nil {
		for _, p := range rep.RTP.Problems {
			fmt.Fprintf(w, "issue: category=%s track=%d line=%d reason=%s\n",
				p.Category, p.Track, p.Line, p.Reason)
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// --- migrate ---------------------------------------------------------------

func runMigrate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "media file to migrate")
	manifestPath := fs.String("manifest", "", "delivery manifest")
	out := fs.String("out", "", "output root for generations and checkpoints")
	stopAfter := fs.String("stop-after", "", "stop after parse, normalize or write")
	resume := fs.Bool("resume", false, "resume from the last valid checkpoint")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if *input == "" || *manifestPath == "" || *out == "" {
		return usageErr(errors.New("migrate requires --input, --manifest and --out"))
	}
	if *stopAfter != "" && migrateStageIndex(*stopAfter) < 0 {
		return usageErr(fmt.Errorf("--stop-after %q must be parse, normalize or write", *stopAfter))
	}
	start := time.Now()
	outcome, err := pipeline.Migrate(pipeline.MigrateOptions{
		Input: *input, ManifestPath: *manifestPath, Out: *out,
		StopAfter: *stopAfter, Resume: *resume,
	})
	if outcome != nil && outcome.Verify != nil {
		printMigrateVerify(stdout, outcome.Verify)
	}
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Milliseconds()
	res := outcome.Result
	fmt.Fprintf(stdout, "resume:       %v\n", res.Resumed)
	if res.Resumed {
		fmt.Fprintf(stdout, "resume-stage: %s\n", orDash(res.ResumeFrom))
		if res.RolledBack {
			fmt.Fprintf(stdout, "rollback:     %s\n", res.Reason)
		} else {
			fmt.Fprintf(stdout, "reason:       %s\n", orDash(res.Reason))
		}
	}
	fmt.Fprintf(stdout, "elapsed-ms:   %d\n", elapsed)
	fmt.Fprintln(stdout, "stages:")
	for _, r := range res.Records {
		state := "ran"
		if r.Skipped {
			state = "reused"
		}
		fmt.Fprintf(stdout, "  %-9s %-6s input=%s output=%s\n",
			r.Stage, state, short(r.InputDigest), short(r.OutputDigest))
	}
	if res.FinalDigest == "" {
		fmt.Fprintf(stdout, "generation:   %s (not committed; stopped after %s)\n",
			res.Generation, orDash(*stopAfter))
		fmt.Fprintln(stdout, "result:       STAGED")
		return nil
	}
	fmt.Fprintf(stdout, "generation:   %s\n", res.Generation)
	fmt.Fprintf(stdout, "sha256:       %s\n", res.FinalDigest)
	fmt.Fprintln(stdout, "result:       OK")
	return nil
}

func printMigrateVerify(w io.Writer, rep *pipeline.VerifyReport) {
	fmt.Fprintf(w, "input:        %s\n", rep.Asset.Path)
	fmt.Fprintf(w, "input-sha256: %s\n", rep.SHA256)
	fmt.Fprintf(w, "elements:     %d, tracks: %d, fragments: %d\n",
		rep.ElementCount, rep.TrackCount, rep.FragmentCount)
	fmt.Fprintf(w, "max-cross-track-deviation: %s\n", timeline.FormatNS(rep.MaxDeviation))
}

func short(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	if d == "" {
		return "-"
	}
	return d
}

// migrateStageIndex mirrors migrate.StageIndex without importing the stage list
// into flag validation twice.
func migrateStageIndex(name string) int {
	switch name {
	case "parse":
		return 0
	case "normalize":
		return 1
	case "write":
		return 2
	}
	return -1
}

// --- error plumbing --------------------------------------------------------

func usageErr(err error) error {
	return &timeline.Error{Stage: timeline.StageParse, Category: timeline.CatIllegalLength,
		Offset: -1, Reason: err.Error()}
}

func ioErr(err error, what string) error {
	return timeline.Wrap(err, timeline.StageCommit, timeline.CatIO, 0, -1, "%s", what)
}
