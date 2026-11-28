// Command rtptrace is the acceptance adapter for the resumable weak-network
// RTP trace regression pipeline. It is a pure terminal program: every action
// is driven by flags and the results are printed as stable key=value lines.
//
// Exit codes: 0 success, 1 internal error, 2 corrupt input.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"rtptrace/internal/fault"
	"rtptrace/internal/fixture"
	"rtptrace/internal/pipeline"
	"rtptrace/internal/stage"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "resume":
		return runResume(args[1:])
	case "corrupt":
		return runCorrupt(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `rtptrace - resumable weak-network RTP trace regression

usage:
  rtptrace demo   --root DIR --seed N [--interrupt-after stage]
  rtptrace resume --root DIR
  rtptrace corrupt --kind truncated-moov --at OFFSET [--root DIR --seed N]

exit codes: 0 ok, 1 internal error, 2 corrupt input
`)
}

type commonFlags struct {
	root           string
	seed           int64
	interruptAfter string
	kind           string
	at             int64
}

func parseFlags(args []string) (commonFlags, error) {
	var f commonFlags
	f.seed = 17
	for i := 0; i < len(args); i++ {
		a := args[i]
		need := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "--root":
			v, err := need()
			if err != nil {
				return f, err
			}
			f.root = v
		case "--seed":
			v, err := need()
			if err != nil {
				return f, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return f, fmt.Errorf("bad --seed %q: %w", v, err)
			}
			f.seed = n
		case "--interrupt-after":
			v, err := need()
			if err != nil {
				return f, err
			}
			f.interruptAfter = v
		case "--kind":
			v, err := need()
			if err != nil {
				return f, err
			}
			f.kind = v
		case "--at":
			v, err := need()
			if err != nil {
				return f, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return f, fmt.Errorf("bad --at %q: %w", v, err)
			}
			f.at = n
		default:
			return f, fmt.Errorf("unknown flag %q", a)
		}
	}
	return f, nil
}

// defaultConfig produces the bounded, deterministic weak-network scenario.
func defaultConfig(inputPath string, seed int64, interrupt string) pipeline.Config {
	return pipeline.Config{
		InputPath:      inputPath,
		Seed:           seed,
		WindowCapacity: 4,
		ReorderEvery:   3,
		DuplicateEvery: 5,
		BurstLoss:      []int{12, 13, 14},
		StartSeq:       100,
		InterruptAfter: interrupt,
	}
}

func runDemo(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		return fail(err, 1)
	}
	if f.root == "" {
		return fail(fmt.Errorf("--root is required"), 1)
	}
	start := time.Now()

	// Generate the bounded MP4 fixture inside the run root.
	specs := []fixture.TrackSpec{
		{TrackID: 1, Timescale: 48000, SampleCount: 24, SampleSize: 20,
			SamplesPerChunk: 6, Delta: 480, SSRC: 0x4000, PayloadType: 96},
		{TrackID: 2, Timescale: 90000, SampleCount: 16, SampleSize: 32,
			SamplesPerChunk: 4, Delta: 3000, SSRC: 0x4001, PayloadType: 97},
	}
	inputDir := filepath.Join(f.root, "input")
	gen, err := fixture.BuildMP4(inputDir, f.seed, specs)
	if err != nil {
		return fail(err, exitFor(err))
	}

	cfg := defaultConfig(gen.Path, f.seed, f.interruptAfter)
	store, err := stage.OpenStore(f.root, nil)
	if err != nil {
		return fail(err, 1)
	}
	st, err := store.Ensure(inputDigestPlaceholder(gen), f.seed, stage.OrderedNames)
	if err != nil {
		return fail(err, exitFor(err))
	}
	runner := stage.NewRunner(store, pipeline.Executors(cfg))
	runner.InterruptAfter = f.interruptAfter
	res, err := runner.Run(st)
	if err != nil {
		return fail(err, exitFor(err))
	}

	if res.Interrupted {
		fmt.Printf("interrupted=true\ninterrupted_stage=%s\n", res.InterruptedName)
		return 0
	}
	return printCompletion(f.root, store, start, false)
}

func runResume(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		return fail(err, 1)
	}
	if f.root == "" {
		return fail(fmt.Errorf("--root is required"), 1)
	}
	start := time.Now()

	store, err := stage.OpenStore(f.root, nil)
	if err != nil {
		return fail(err, 1)
	}
	st, err := store.Load()
	if err != nil {
		return fail(err, exitFor(err))
	}
	if st == nil {
		return fail(fmt.Errorf("no run state under %s; run demo first", f.root), 1)
	}
	// Locate the generated input from the first run.
	inputPath := filepath.Join(f.root, "input", "trace.mp4")
	cfg := defaultConfig(inputPath, st.Seed, "")
	runner := stage.NewRunner(store, pipeline.Executors(cfg))
	res, err := runner.Run(st)
	if err != nil {
		return fail(err, exitFor(err))
	}
	return printCompletion(f.root, store, start, res.Recovered)
}

func runCorrupt(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		return fail(err, 1)
	}
	if f.kind != "truncated-moov" {
		return fail(fmt.Errorf("unsupported --kind %q", f.kind), 1)
	}
	if f.root == "" {
		f.root = filepath.Join(os.TempDir(), "rtptrace-corrupt")
	}
	specs := []fixture.TrackSpec{
		// 200 samples place moov just before offset 4096, so a truncation at
		// 4096 lands inside the moov box and yields a locatable bad_offset.
		{TrackID: 1, Timescale: 48000, SampleCount: 200, SampleSize: 20,
			SamplesPerChunk: 10, Delta: 480, SSRC: 0x4000, PayloadType: 96},
	}
	gen, err := fixture.BuildMP4(filepath.Join(f.root, "input"), f.seed, specs)
	if err != nil {
		return fail(err, exitFor(err))
	}
	dst := filepath.Join(f.root, "corrupt.mp4")
	if err := fixture.TruncateCopy(gen.Path, dst, f.at); err != nil {
		return fail(err, exitFor(err))
	}
	// Scan the damaged file purely to report the located bad offset.
	cfg := defaultConfig(dst, f.seed, "")
	store, _ := stage.OpenStore(filepath.Join(f.root, "corrupt-run"), nil)
	st, _ := store.Ensure("corrupt", f.seed, stage.OrderedNames)
	runner := stage.NewRunner(store, pipeline.Executors(cfg))
	if _, err := runner.Run(st); err != nil {
		if fe, ok := fault.AsError(err); ok {
			fmt.Printf("ok=false\nkind=%s\nbad_offset=%d\n", fe.Kind, fe.Offset)
			return 2
		}
		return fail(err, 1)
	}
	// A truncation that happened to stay parseable is still a failed check.
	return fail(fmt.Errorf("corruption at %d was not detected", f.at), 1)
}

// printCompletion promotes the merge artifact into manifest.json + artifact.bin
// and prints the stable verification block.
func printCompletion(root string, store *stage.Store, start time.Time, recovered bool) int {
	st, err := store.Load()
	if err != nil {
		return fail(err, 1)
	}
	rec, ok := st.Stages[stage.NameMerge]
	if !ok {
		return fail(fmt.Errorf("merge stage missing"), 1)
	}
	bundleBytes, err := store.ReadArtifact(rec)
	if err != nil {
		return fail(err, exitFor(err))
	}
	var bundle struct {
		Digest   string `json:"artifact_sha256"`
		Manifest string `json:"manifest"`
		Artifact string `json:"artifact_hex"`
	}
	if err := json.Unmarshal(bundleBytes, &bundle); err != nil {
		return fail(err, 1)
	}
	artifact, err := hex.DecodeString(bundle.Artifact)
	if err != nil {
		return fail(err, 1)
	}
	manifestPath := filepath.Join(root, pipeline.ManifestName)
	artifactPath := filepath.Join(root, pipeline.ArtifactBinName)
	if err := os.WriteFile(manifestPath, []byte(bundle.Manifest), 0o644); err != nil {
		return fail(err, 1)
	}
	if err := os.WriteFile(artifactPath, artifact, 0o644); err != nil {
		return fail(err, 1)
	}

	manifestSHA := pipeline.DigestBytes([]byte(bundle.Manifest))
	artifactSHA := pipeline.DigestBytes(artifact)
	if artifactSHA != bundle.Digest {
		return fail(fmt.Errorf("artifact digest mismatch after write"), 1)
	}

	// Pull counters from the rtp stage artifact.
	rtpRec := st.Stages[stage.NameRTP]
	rtpBytes, err := store.ReadArtifact(rtpRec)
	if err != nil {
		return fail(err, 1)
	}
	var rtpRes pipeline.RTPResult
	if err := json.Unmarshal(rtpBytes, &rtpRes); err != nil {
		return fail(err, 1)
	}

	elapsedMS := time.Since(start).Milliseconds()
	fmt.Printf("recovered=%v\n", recovered)
	fmt.Printf("resume_ms=%d\n", elapsedMS)
	fmt.Printf("lost_packets=%d\n", rtpRes.Stats.LostPackets)
	fmt.Printf("duplicate_packets=%d\n", rtpRes.Stats.Duplicates)
	fmt.Printf("manifest_sha256=%s\n", manifestSHA)
	fmt.Printf("artifact_sha256=%s\n", artifactSHA)
	return 0
}

func inputDigestPlaceholder(_ *fixture.MP4Fixture) string {
	// The scan executor computes the real digest from the file; state only
	// needs a stable non-empty seed label up front.
	return "pending-scan"
}

func fail(err error, code int) int {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return code
}

func exitFor(err error) int {
	if fault.IsCorrupt(err) {
		return 2
	}
	return 1
}
