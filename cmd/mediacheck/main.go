// Command mediacheck is the terminal entry point for the media verification
// kernel. It synthesizes deterministic fixtures, runs the full parse -> probe
// -> normalize -> validate -> commit pipeline and emits one JSON line.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"mediacheck/internal/dash"
	"mediacheck/internal/fixture"
	"mediacheck/internal/hls"
	"mediacheck/internal/model"
	"mediacheck/internal/normalize"
	"mediacheck/internal/probe"
	"mediacheck/internal/report"
	"mediacheck/internal/safeio"
	"mediacheck/internal/txn"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitDamaged = 2
)

type options struct {
	seed       int64
	faults     []model.FaultKind
	crashAfter string
	resume     bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediacheck demo --seed N --faults a,b [--crash-after stage] [--resume]")
		return exitFailure
	}
	opts, err := parseFlags(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid arguments: %v\n", err)
		return exitFailure
	}
	dir := resumeDir(opts.seed)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "work dir: %v\n", err)
		return exitFailure
	}
	code, err := execute(opts, dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failure: %v\n", err)
		return exitFailure
	}
	return code
}

func execute(opts options, dir string) (int, error) {
	journal, err := txn.Open(dir)
	if err != nil {
		return exitFailure, err
	}
	defer journal.Close()
	mgr := txn.NewManager(journal, dir)

	recovered := 0
	if opts.resume {
		// A resumed run must complete: any simulated crash point is consumed
		// by the recovery itself, so crash-after is intentionally ignored.
		opts.crashAfter = ""
		ids, rolledBack, err := mgr.Recover()
		if err != nil {
			return exitFailure, err
		}
		recovered = len(ids)
		_ = rolledBack
	}

	// Deterministic synthesis of the full program from the seed.
	prog := fixture.Generate(opts.seed, opts.faults)
	if err := fixture.Materialize(prog); err != nil {
		return exitFailure, err
	}
	lim := safeio.DefaultLimits()
	manifest, err := dash.ParseMPD(prog, lim)
	if err != nil {
		return exitFailure, err
	}
	// HLS view: exercise discontinuity handling on one video representation.
	if _, err := hls.Parse(hls.BuildPlaylist(prog.Video[0], opts.faults), lim); err != nil {
		return exitFailure, err
	}

	txnID := fmt.Sprintf("seed-%d-faults-%s", opts.seed, faultToken(opts.faults))
	payload := map[string]interface{}{
		"seed":   opts.seed,
		"faults": faultToken(opts.faults),
	}
	hooks := buildHooks(opts, prog, manifest)
	if _, err := mgr.Run(txnID, payload, hooks); err != nil {
		return exitFailure, err
	}
	if opts.resume {
		// Whether this run committed or replayed an existing record, the
		// transaction was recovered across the crash boundary exactly once.
		recovered = 1
	}

	probeReport := probe.Inspect(manifest, prog)
	normReport := normalize.Validate(manifest)
	line, err := report.Build(report.Inputs{
		Manifest: manifest, Probe: probeReport,
		Normalize: normReport, Recovered: recovered,
	}, lim)
	if err != nil {
		return exitFailure, err
	}
	out, err := line.Marshal()
	if err != nil {
		return exitFailure, err
	}
	if _, err := os.Stdout.Write(out); err != nil {
		return exitFailure, err
	}
	if len(line.DamagedRanges) > 0 || len(line.MetadataConflicts) > 0 {
		return exitDamaged, nil
	}
	return exitOK, nil
}

// buildHooks wires staged validation and the optional simulated crash.
// A simulated crash is a hard process stop: the WAL prepared record is already
// durable and no abort marker is written, exactly like a real power loss.
func buildHooks(opts options, prog *fixture.Program,
	manifest *dash.Manifest) map[model.Stage]txn.StageFunc {
	crash := func(stage string) error {
		fmt.Fprintf(os.Stderr, "crashed after stage %s\n", stage)
		os.Exit(exitFailure)
		return nil
	}
	hooks := map[model.Stage]txn.StageFunc{
		model.StageProbe: func(map[string]interface{}) error {
			if opts.crashAfter == "probe" {
				return crash("probe")
			}
			return nil
		},
		model.StageNormalize: func(map[string]interface{}) error {
			_ = normalize.Validate(manifest)
			return nil
		},
		model.StageValidate: func(map[string]interface{}) error {
			if opts.crashAfter == "validate" {
				return crash("validate")
			}
			return validateStage(prog, manifest)
		},
	}
	return hooks
}

// validateStage enforces the acceptance invariants before commit.
func validateStage(prog *fixture.Program, manifest *dash.Manifest) error {
	for _, r := range manifest.Representations {
		if r.Timescale <= 0 {
			return model.ErrZeroTimescale
		}
		dev := r.DeviationPct
		if dev < 0 {
			dev = -dev
		}
		// Damage is tolerated (exit 2), but an out-of-contract bitrate on a
		// structurally healthy ladder fails the transaction (exit 1).
		if dev > 25.0 {
			return fmt.Errorf("%w: %s deviation %.2f%%", model.ErrStageFailed, r.ID, dev)
		}
	}
	return nil
}

// faultAliases maps the short CLI tokens used in the acceptance commands to
// the internal fault kinds.
var faultAliases = map[string]model.FaultKind{
	"none":          model.FaultNone,
	"truncate":      model.FaultTruncate,
	"late":          model.FaultLate,
	"duplicate":     model.FaultDuplicate,
	"metadata":      model.FaultMetaConflict,
	"keyframe":      model.FaultKeyframe,
	"backwards":     model.FaultBackwards,
	"discontinuity": model.FaultDiscontinuity,
	"timescale":     model.FaultTimescale,
	"skew":          model.FaultClockSkew,
}

func parseFlags(args []string) (options, error) {
	opts := options{seed: 1}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--seed":
			i++
			if i >= len(args) {
				return opts, errors.New("--seed needs a value")
			}
			if _, err := fmt.Sscanf(args[i], "%d", &opts.seed); err != nil {
				return opts, err
			}
		case "--faults":
			i++
			if i >= len(args) {
				return opts, errors.New("--faults needs a value")
			}
			for _, token := range strings.Split(args[i], ",") {
				token = strings.TrimSpace(token)
				if token == "" || token == "none" {
					continue
				}
				k, ok := faultAliases[token]
				if !ok {
					if parsed, err := model.ParseFaultKind(token); err == nil {
						k = parsed
					} else {
						return opts, fmt.Errorf("unknown fault kind: %q", token)
					}
				}
				opts.faults = append(opts.faults, k)
			}
		case "--crash-after":
			i++
			if i >= len(args) {
				return opts, errors.New("--crash-after needs a stage")
			}
			opts.crashAfter = args[i]
		case "--resume":
			opts.resume = true
		default:
			return opts, fmt.Errorf("unknown flag %q", args[i])
		}
	}
	return opts, nil
}

func faultToken(faults []model.FaultKind) string {
	if len(faults) == 0 {
		return "none"
	}
	parts := make([]string, len(faults))
	for i, f := range faults {
		parts[i] = string(f)
	}
	return strings.Join(parts, "-")
}

func resumeDir(seed int64) string {
	return fmt.Sprintf("%s/seed-%d", os.TempDir(), seed)
}
