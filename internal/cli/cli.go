// Package cli implements the demo and verify subcommands. It formats the
// terminal report but leaves every media decision to the pipeline.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mediadeliver/internal/align"
	"mediadeliver/internal/fixture"
	"mediadeliver/internal/model"
	"mediadeliver/internal/pipeline"
)

// Exit codes are part of the acceptance contract.
const (
	ExitOK          = 0
	ExitInputError  = 2
	ExitResumeStale = 3
	ExitManifestBad = 4
)

// Main dispatches a subcommand and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return ExitInputError
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "mediadeliver: unknown subcommand %q\n", args[0])
		usage(stderr)
		return ExitInputError
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage:
  mediadeliver demo   --work DIR [--faults none|all]
  mediadeliver verify --work DIR --manifest FILE

demo writes fixture inputs, runs the pipeline (resuming once to prove the
cursor works) and prints the summary fields.
verify recomputes the manifest SHA-256 and re-checks every invariant.
`)
}

type flags struct {
	work     string
	faults   string
	manifest string
}

// parseFlags accepts only the long options used by the acceptance commands.
func parseFlags(args []string) (flags, error) {
	var f flags
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return f, fmt.Errorf("unexpected argument %q", a)
		}
		name := strings.TrimPrefix(a, "--")
		value := ""
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, value = name[:eq], name[eq+1:]
		} else {
			if i+1 >= len(args) {
				return f, fmt.Errorf("--%s needs a value", name)
			}
			i++
			value = args[i]
		}
		switch name {
		case "work":
			f.work = value
		case "faults":
			f.faults = value
		case "manifest":
			f.manifest = value
		default:
			return f, fmt.Errorf("unknown option --%s", name)
		}
	}
	return f, nil
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "demo:", err)
		return ExitInputError
	}
	if f.work == "" {
		fmt.Fprintln(stderr, "demo: --work is required")
		return ExitInputError
	}
	if f.faults == "" {
		f.faults = "none"
	}
	if f.faults != "none" && f.faults != "all" {
		fmt.Fprintf(stderr, "demo: unknown --faults %q (want none or all)\n", f.faults)
		return ExitInputError
	}
	work, err := filepath.Abs(f.work)
	if err != nil {
		fmt.Fprintln(stderr, "demo:", err)
		return ExitInputError
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		fmt.Fprintln(stderr, "demo:", err)
		return ExitInputError
	}

	normal := fixture.Normal()
	if code := demoNormal(work, normal, stdout, stderr); code != ExitOK {
		return code
	}
	if f.faults == "all" {
		if code := demoDamaged(work, stdout, stderr); code != ExitOK {
			return code
		}
	}
	return ExitOK
}

func demoNormal(work string, d fixture.Delivery, stdout, stderr io.Writer) int {
	mpd, pl, err := pipeline.WriteInputs(work, d.MPD, d.Playlist, d.Media)
	if err != nil {
		fmt.Fprintln(stderr, "demo:", err)
		return ExitInputError
	}
	manifestPath := filepath.Join(work, "manifest.json")
	opts := pipeline.Options{WorkDir: work, MPDPath: mpd, HLSPath: pl,
		MediaPaths: mediaPaths(work, d), ManifestPath: manifestPath}

	// Both passes honour an existing cursor, so a re-run over changed
	// inputs is rejected with the stale-cursor exit code instead of
	// silently mixing old and new results.
	first := withResume(opts, true)
	first.InterruptAt = pipeline.StageVerify
	// First pass stops before verification, proving the cursor is written
	// before the expensive stage completes.
	if _, err := pipeline.Run(first); err != nil {
		if !isInputChanged(err) {
			return reportRunError("demo", err, stderr)
		}
		// The demo regenerates its fixtures; if they differ from the run
		// that wrote the cursor, discard the cursor and start cleanly
		// rather than reuse stale results.
		_ = os.Remove(filepath.Join(work, "resume.json"))
		_ = os.Remove(manifestPath)
		if _, err := pipeline.Run(first); err != nil {
			return reportRunError("demo", err, stderr)
		}
	}
	resumeFrom := cursorStage(work)
	resumeStart := time.Now()
	out, err := pipeline.Run(withResume(opts, true))
	if err != nil {
		return reportRunError("demo", err, stderr)
	}
	resumeMS := time.Since(resumeStart).Milliseconds()
	if !out.Published {
		fmt.Fprintln(stderr, "demo: normal fixture did not publish")
		for _, e := range out.Diagnostics {
			fmt.Fprintf(stderr, "  %v\n", e)
		}
		return ExitInputError
	}
	sha, err := fileSHA(manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "demo:", err)
		return ExitInputError
	}
	summary := out.Manifest.Summary
	maxDriftMS := summary.MaxDriftNS / 1_000_000
	if summary.MaxDriftNS%1_000_000 != 0 {
		maxDriftMS++
	}
	fmt.Fprintf(stdout, "representations=%d\n", summary.Representations)
	fmt.Fprintf(stdout, "segments=%d\n", summary.Segments)
	fmt.Fprintf(stdout, "align_error_ns=%d\n", summary.MaxDriftNS)
	fmt.Fprintf(stdout, "max_drift_ms=%d\n", maxDriftMS)
	fmt.Fprintf(stdout, "resume_from_stage=%s\n", resumeFrom)
	fmt.Fprintf(stdout, "resume_ms=%d\n", resumeMS)
	fmt.Fprintf(stdout, "manifest_sha256=%s\n", sha)
	return ExitOK
}

func demoDamaged(work string, stdout, stderr io.Writer) int {
	d := fixture.Damaged()
	dir := filepath.Join(work, "damaged")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(stderr, "demo:", err)
		return ExitInputError
	}
	mpd, pl, err := pipeline.WriteInputs(dir, d.MPD, d.Playlist, d.Media)
	if err != nil {
		fmt.Fprintln(stderr, "demo:", err)
		return ExitInputError
	}
	opts := pipeline.Options{WorkDir: dir, MPDPath: mpd, HLSPath: pl,
		MediaPaths: mediaPaths(dir, d), ManifestPath: filepath.Join(dir, "manifest.json")}
	out, err := pipeline.Run(opts)
	if err != nil {
		return reportRunError("demo", err, stderr)
	}
	if out.Published {
		fmt.Fprintln(stderr, "demo: damaged fixture unexpectedly published a manifest")
		return ExitManifestBad
	}
	seen := map[model.Code]bool{}
	var ordered []*model.Error
	for _, e := range out.Diagnostics {
		if !seen[e.Code] {
			seen[e.Code] = true
			ordered = append(ordered, e)
		}
	}
	faultCount := 0
	for _, want := range []model.Code{model.CodeTruncatedBox, model.CodeInvalidBoxSize,
		model.CodeZeroTimescale, model.CodeDuplicateMediaSeq, model.CodeMissingIndex} {
		found := false
		for _, e := range out.Diagnostics {
			if e.Code == want {
				found = true
				fmt.Fprintf(stdout, "fault=%s source_offset=%d path=%s\n", e.Code, e.Offset, e.Path)
				break
			}
		}
		if found {
			faultCount++
		}
	}
	fmt.Fprintf(stdout, "faults=%d\n", faultCount)
	// offset_error_bytes is the distance between the reported source offset
	// and the independently located defect byte in the raw input.
	if want := faultTimescaleOffset(dir); want >= 0 {
		got := timescaleFaultOffset(out.Diagnostics)
		diff := int64(0)
		if got >= 0 {
			diff = got - want
			if diff < 0 {
				diff = -diff
			}
		}
		fmt.Fprintf(stdout, "offset_error_bytes=%d\n", diff)
	}
	if faultCount != len(fixture.AllFaults) {
		fmt.Fprintf(stderr, "demo: expected %d fault classes, saw %d\n", len(fixture.AllFaults), faultCount)
		return ExitInputError
	}
	return ExitOK
}

// faultTimescaleOffset finds the zero-timescale attribute in the damaged
// MPD without consulting the parser output at all.
func faultTimescaleOffset(dir string) int64 {
	data, err := os.ReadFile(filepath.Join(dir, "inputs", "manifest.mpd"))
	if err != nil {
		return -1
	}
	i := strings.Index(string(data), `timescale="0"`)
	if i < 0 {
		return -1
	}
	return int64(i)
}

func timescaleFaultOffset(errs []*model.Error) int64 {
	for _, e := range errs {
		if e.Code == model.CodeZeroTimescale {
			return e.Offset
		}
	}
	return -1
}

// isInputChanged reports whether err is the stale-cursor refusal.
func isInputChanged(err error) bool {
	var e *model.Error
	return asError(err, &e) && e.Code == model.CodeInputChanged
}

// cursorStage reports the stage recorded in the resume cursor, for display.
func cursorStage(work string) string {
	data, err := os.ReadFile(filepath.Join(work, "resume.json"))
	if err != nil {
		return "none"
	}
	var cp struct {
		Stage string `json:"stage"`
	}
	if err := json.Unmarshal(data, &cp); err != nil || cp.Stage == "" {
		return "none"
	}
	return cp.Stage
}

// mediaPaths maps every representation to the fixture file written by
// WriteInputs, so scanning streams from disk instead of from memory.
func mediaPaths(dir string, d fixture.Delivery) map[string]string {
	paths := map[string]string{}
	for id := range d.Media {
		paths[id] = filepath.Join(dir, "inputs", id+".mp4")
	}
	return paths
}

func withInterrupt(o pipeline.Options, stage string) pipeline.Options {
	o.InterruptAt = stage
	return o
}

func withResume(o pipeline.Options, resume bool) pipeline.Options {
	o.Resume = resume
	return o
}

func reportRunError(prefix string, err error, stderr io.Writer) int {
	fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
	var e *model.Error
	if asError(err, &e) {
		switch e.Code {
		case model.CodeInputChanged, model.CodeResumeInconsistent:
			return ExitResumeStale
		}
	}
	return ExitInputError
}

func asError(err error, target **model.Error) bool {
	for err != nil {
		if e, ok := err.(*model.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func fileSHA(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintln(stderr, "verify:", err)
		return ExitInputError
	}
	if f.work == "" || f.manifest == "" {
		fmt.Fprintln(stderr, "verify: --work and --manifest are required")
		return ExitInputError
	}
	work, err := filepath.Abs(f.work)
	if err != nil {
		fmt.Fprintln(stderr, "verify:", err)
		return ExitInputError
	}
	manifestPath, err := filepath.Abs(f.manifest)
	if err != nil {
		fmt.Fprintln(stderr, "verify:", err)
		return ExitInputError
	}
	if rel, err := filepath.Rel(work, manifestPath); err != nil || strings.HasPrefix(rel, "..") {
		fmt.Fprintln(stderr, "verify: manifest is outside the work directory")
		return ExitInputError
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, "verify:", err)
		return ExitInputError
	}
	var m align.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		// Malformed JSON is an input/format error, not a failed check.
		fmt.Fprintf(stderr, "verify: manifest is not valid JSON: %v\n", err)
		return ExitInputError
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if errs := pipeline.Verify(&m); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(stderr, "verify: %v\n", e)
		}
		return ExitManifestBad
	}
	if !m.Summary.Deliverable {
		fmt.Fprintln(stderr, "verify: manifest is explicitly not deliverable")
		fmt.Fprintf(stdout, "manifest_sha256=%s\n", sha)
		return ExitManifestBad
	}
	if err := checkTraceability(work, &m); err != nil {
		fmt.Fprintf(stderr, "verify: %v\n", err)
		return ExitManifestBad
	}
	fmt.Fprintf(stdout, "manifest_sha256=%s\n", sha)
	fmt.Fprintf(stdout, "representations=%d\n", m.Summary.Representations)
	fmt.Fprintf(stdout, "segments=%d\n", m.Summary.Segments)
	fmt.Fprintf(stdout, "intervals=%d\n", m.Summary.Intervals)
	fmt.Fprintf(stdout, "max_drift_ms=%d\n", (m.Summary.MaxDriftNS+999_999)/1_000_000)
	fmt.Fprintln(stdout, "verified=true")
	return ExitOK
}

// checkTraceability re-hashes every input recorded in the manifest. A
// manifest that no longer matches its inputs must not verify.
func checkTraceability(work string, m *align.Manifest) error {
	for _, in := range m.Inputs {
		path := filepath.Join(work, filepath.FromSlash(in.Path))
		sha, err := fileSHA(path)
		if err != nil {
			return fmt.Errorf("input %s is missing: %w", in.Path, err)
		}
		if sha != in.SHA256 {
			return fmt.Errorf("input %s changed (sha256 %s, manifest has %s)", in.Path, sha, in.SHA256)
		}
	}
	return nil
}
