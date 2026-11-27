// Command media-check is the acceptance adapter for the media processing
// kernel. It runs the end-to-end pipeline on self-generated RTP/RTCP
// fixtures and prints the verifiable structural, damage, recovery and
// rollback metrics required by the acceptance scenario.
//
// Exit codes:
//
//	0 - all assertions passed
//	2 - input or time-base error
//	3 - recovery / ledger invariant failure
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"media-check/internal/fault"
	"media-check/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: media-check demo [flags]")
		return 2
	}
	cfg, err := parseFlags(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "input error: %v\n", err)
		return 2
	}
	res, err := pipeline.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline failed: %v\n", err)
		if errors.Is(err, fault.ErrTimebase) {
			return 2
		}
		return 3
	}

	printReport(res)

	// Exit-code mapping driven by the shared fault model and ledger results.
	if tb := res.Faults.TimebaseFaults(); len(tb) > 0 {
		fmt.Printf("RESULT: time-base error (%d ambiguous wrap)\n", len(tb))
		return 2
	}
	if invariantBroken(res) {
		fmt.Println("RESULT: recovery/ledger invariant failure")
		return 3
	}
	fmt.Println("RESULT: all assertions passed")
	return 0
}

// cliConfig mirrors pipeline.Config with flag-friendly scalar fields.
type cliConfig struct {
	pipeline.Config
	lossCSV    string
	rollbackTo string
}

func parseFlags(flags []string) (pipeline.Config, error) {
	c := cliConfig{Config: pipeline.Config{Seed: 17, WorkDir: ".media-check-work"}}
	for i := 0; i < len(flags); i++ {
		arg := flags[i]
		next := func() (string, error) {
			if i+1 >= len(flags) {
				return "", fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			return flags[i], nil
		}
		switch arg {
		case "--seed":
			v, err := next()
			if err != nil {
				return c.Config, err
			}
			seed, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return c.Config, fmt.Errorf("--seed: %w", err)
			}
			c.Seed = seed
		case "--loss":
			v, err := next()
			if err != nil {
				return c.Config, err
			}
			c.lossCSV = v
		case "--reorder":
			v, err := next()
			if err != nil {
				return c.Config, err
			}
			d, err := strconv.Atoi(v)
			if err != nil {
				return c.Config, fmt.Errorf("--reorder: %w", err)
			}
			c.ReorderDistance = d
		case "--wrap-seq":
			v, err := next()
			if err != nil {
				return c.Config, err
			}
			w, err := strconv.ParseUint(v, 10, 16)
			if err != nil {
				return c.Config, fmt.Errorf("--wrap-seq: %w", err)
			}
			c.WrapStartSeq = uint16(w)
		case "--crash-after-artifact":
			c.CrashAfterArtifact = true
		case "--rerun":
			c.Rerun = true
		case "--rollback-to":
			v, err := next()
			if err != nil {
				return c.Config, err
			}
			c.rollbackTo = v
		case "--workdir":
			v, err := next()
			if err != nil {
				return c.Config, err
			}
			c.WorkDir = v
		default:
			return c.Config, fmt.Errorf("unknown flag %q", arg)
		}
	}
	if c.lossCSV != "" {
		units, err := parseLoss(c.lossCSV)
		if err != nil {
			return c.Config, err
		}
		// Acceptance fixture applies loss to the video track (id 1).
		c.LossUnits = map[uint32][]int{1: units}
	}
	c.RollbackTarget = c.rollbackTo
	return c.Config, nil
}

// parseLoss accepts a comma list such as "0,1,8".
func parseLoss(csv string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("--loss entry %q: %w", part, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func invariantBroken(res *pipeline.Result) bool {
	r := res.RecoveryReport
	// A quarantine means an artifact failed digest verification.
	return r.Quarantined > 0
}

func printReport(res *pipeline.Result) {
	fmt.Println("=== media-check run ===")
	fmt.Printf("tracks: %d\n", res.TrackCount)
	fmt.Printf("key frames: %d\n", res.KeyFrameCount)
	fmt.Printf("A/V sync max: %d ms, P95: %d ms\n", res.SyncMaxMillis, res.SyncP95Millis)
	fmt.Printf("processed units: %d, P95 latency: %d ms\n",
		res.Stats.ProcessedUnits, res.Stats.P95LatencyMS)
	fmt.Printf("structure digest: %s\n", res.StructureDigest)
	fmt.Printf("damage digest: %s\n", res.DamageDigest)
	fmt.Printf("source digest: %s\n", short(res.SourceDigest))
	fmt.Printf("derive digest: %s\n", short(res.DeriveDigest))

	if len(res.DamageSegments) == 0 {
		fmt.Println("damage segments: none")
	} else {
		fmt.Println("damage segments:")
		for _, s := range res.DamageSegments {
			fmt.Printf("  - track=%d %d..%d ms first_err_offset=%d seq=%d..%d codes=%s\n",
				s.TrackID, s.Start.Millis(), s.End.Millis(),
				s.FirstErrOffset, s.SeqStart, s.SeqEnd, joinCodes(s.Codes))
		}
	}
	fmt.Printf("loss recovery: %d/%d = %.3f\n",
		res.Recovery.Recovered, res.Recovery.Recoverable, res.Recovery.Rate)

	r := res.RecoveryReport
	fmt.Printf("rerun reuse: %d, republish: %d, missed-ledger: %d, quarantined: %d\n",
		r.Reused, r.Republished, r.MissedLedger, r.Quarantined)
	if res.RollbackVersion != "" {
		fmt.Printf("rollback: %s manifest=%s\n", res.RollbackVersion, short(res.RollbackManifest))
	}
}

func joinCodes(codes []fault.Code) string {
	parts := make([]string, len(codes))
	for i, c := range codes {
		parts[i] = string(c)
	}
	return strings.Join(parts, ",")
}

func short(d string) string {
	if len(d) <= 16 {
		return d
	}
	return d[:16] + "..."
}
