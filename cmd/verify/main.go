// Command verify is the terminal acceptance entry point.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"example.com/archiveverify/internal/model"
	"example.com/archiveverify/internal/verify"
)

func main() {
	os.Exit(run())
}

func run() int {
	caseName := flag.String("case", "clean", "fixture case: clean or faults")
	rangeSpec := flag.String("range", "2s:3s", "half-open range, e.g. 2s:3s")
	iterations := flag.Int("iterations", 100, "read latency iterations")
	mem := flag.String("mem", "8MiB", "pipeline memory budget, e.g. 8MiB")
	queue := flag.Int("queue", 4, "pipeline queue capacity")
	resume := flag.Bool("resume", false, "use fingerprinted checkpoint/resume")
	repeat := flag.Int("repeat", 1, "repeat build and compare index digests")
	workDir := flag.String("workdir", "", "generated input/checkpoint directory")
	input := flag.String("input", "", "use an existing manifest instead of generating a fixture")
	flag.Parse()

	memBytes, err := parseSize(*mem)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid -mem:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rep, err := verify.Run(ctx, verify.Config{
		Case: *caseName, Range: *rangeSpec, WorkDir: *workDir, ManifestPath: *input,
		Iterations: *iterations, MemoryBytes: memBytes, Queue: *queue,
		Resume: *resume, Repeat: *repeat,
	})
	if rep != nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify failed: %v\n", err)
		return model.ExitCode(err)
	}
	if err := checkAcceptance(rep); err != nil {
		fmt.Fprintf(os.Stderr, "acceptance threshold failed: %v\n", err)
		return 1
	}
	return 0
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "KiB"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "KiB")
	case strings.HasSuffix(s, "MiB"):
		mult = 1 << 20
		s = strings.TrimSuffix(s, "MiB")
	case strings.HasSuffix(s, "GiB"):
		mult = 1 << 30
		s = strings.TrimSuffix(s, "GiB")
	case strings.HasSuffix(s, "KB"):
		mult = 1000
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"):
		mult = 1_000_000
		s = strings.TrimSuffix(s, "MB")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if n > (1<<62)/mult {
		return 0, fmt.Errorf("size overflow %q", s)
	}
	return n * mult, nil
}

func checkAcceptance(r *verify.Report) error {
	if !r.DigestEqual {
		return errors.New("repeated index digests differ")
	}
	if r.PeakMiB > 8 {
		return fmt.Errorf("peak_mib=%.3f exceeds 8", r.PeakMiB)
	}
	switch r.Case {
	case "clean":
		if r.SyncMS > 1 {
			return fmt.Errorf("sync_ms=%.3f exceeds 1", r.SyncMS)
		}
	case "faults":
		if r.LossRecoveryPct != 75 {
			return fmt.Errorf("loss_recovery_pct=%.1f want 75", r.LossRecoveryPct)
		}
		if r.SyncMS > 5 {
			return fmt.Errorf("sync_ms=%.3f exceeds 5", r.SyncMS)
		}
		if r.P95MS > 250 {
			return fmt.Errorf("p95_ms=%.3f exceeds 250", r.P95MS)
		}
		if r.Quarantined != 1 {
			return fmt.Errorf("quarantined=%d want 1", r.Quarantined)
		}
		if r.MFHDGaps != 1 || r.TFDTRepairs < 1 || len(r.OffsetTrace) == 0 {
			return errors.New("fault audit records missing")
		}
		hasOriginal := false
		for _, t := range r.OffsetTrace {
			if t.Original != 0 || t.OrigRaw != 0 {
				hasOriginal = true
			}
		}
		if !hasOriginal {
			return errors.New("offset trace lacks original value")
		}
	}
	return nil
}
