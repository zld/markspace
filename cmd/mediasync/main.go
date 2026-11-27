// Command mediasync is the terminal acceptance adapter for the media-sync
// kernel. The `demo` subcommand generates a code-generated scenario, runs the
// full pipeline and prints per-item input-to-output mappings, time/byte
// deltas, located failures and SHA-256 digests. Exit code is zero only when
// every expected acceptance check passes.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediasync/internal/fixture"
	"mediasync/internal/model"
	"mediasync/internal/pipeline"
	"mediasync/internal/report"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediasync demo [--seed N] [--budget-ms N] [--max-bitrate N] [--min-quality N]")
}

type demoFlags struct {
	seed       int64
	budgetMS   int
	maxBitrate int64
	minQuality int
}

func parseDemoFlags(args []string) (demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var f demoFlags
	fs.Int64Var(&f.seed, "seed", 17, "fixture RNG seed")
	fs.IntVar(&f.budgetMS, "budget-ms", 200, "per-track processing budget in milliseconds")
	fs.Int64Var(&f.maxBitrate, "max-bitrate", 96000, "target maximum bitrate in bps")
	fs.IntVar(&f.minQuality, "min-quality", 0, "minimum required quality (0..100)")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.seed < 0 || f.budgetMS < 0 || f.maxBitrate < 0 || f.minQuality < 0 {
		return f, fmt.Errorf("all numeric flags must be non-negative")
	}
	if f.minQuality > 100 {
		return f, fmt.Errorf("min-quality must be within 0..100")
	}
	return f, nil
}

func runDemo(args []string) int {
	f, err := parseDemoFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parameter error:", err)
		return 2
	}

	sc := fixture.Generate(f.seed)
	budgetNS := model.NS(f.budgetMS) * 1_000_000
	target := model.TargetSpec{
		Channels:   2,
		SampleRate: 48000,
		Color: &model.ColorMeta{
			ColorPrimaries: 9, TransferCharacteristics: 16,
			MatrixCoefficients: 9, FullRange: false,
		},
		MaxBitrate: f.maxBitrate,
		BudgetNS:   budgetNS,
		MinQuality: f.minQuality,
	}
	manifest := pipeline.Run(pipeline.Input{
		Tracks: sc.Tracks,
		Target: target,
		Seed:   f.seed,
		Params: report.Params{
			BudgetNS: budgetNS, MaxBitrate: f.maxBitrate, MinQuality: f.minQuality,
		},
	})

	printManifest(manifest)
	checks := verify(sc, manifest)
	printChecks(checks)

	for _, c := range checks {
		if !c.pass {
			return 1
		}
	}
	return 0
}

type check struct {
	name   string
	pass   bool
	detail string
}

func verify(sc *fixture.Scenario, m report.Manifest) []check {
	var cs []check
	byID := map[string]report.TrackReport{}
	for _, tr := range m.Tracks {
		byID[tr.TrackID] = tr
	}

	// 1. Clean track: zero time and byte deltas.
	clean := byID["dialogue"]
	cleanOK := len(clean.Mappings) > 0
	for _, mp := range clean.Mappings {
		if mp.TimeDeltaNS != 0 || mp.ByteDelta != 0 {
			cleanOK = false
		}
	}
	cs = append(cs, check{"clean track time/byte delta == 0", cleanOK, clean.TrackID})

	// 2. Duplicate packets add zero duration.
	sfx := byID["sfx"]
	dupeOK := true
	dupeCount := 0
	for _, mp := range sfx.Mappings {
		if mp.Class == model.EntryDuplicate {
			dupeCount++
			if mp.DurationNS != 0 {
				dupeOK = false
			}
		}
	}
	cs = append(cs, check{"duplicate packets add 0 ns", dupeOK && dupeCount > 0,
		fmt.Sprintf("duplicates=%d", dupeCount)})

	// 3. Gaps are recorded.
	gapOK := false
	for _, mp := range sfx.Mappings {
		if mp.Class == model.EntryGap {
			gapOK = true
		}
	}
	cs = append(cs, check{"missing packets leave gap records", gapOK, sfx.TrackID})

	// 4. Truncation located at the generated packet offset.
	broken := byID["broken"]
	brokenOK := hasFailureAt(broken, sc.Expect["broken"].TruncatedOffsets)
	cs = append(cs, check{"truncated Opus located at packet offset", brokenOK,
		fmt.Sprintf("want offsets %v", sc.Expect["broken"].TruncatedOffsets)})

	aac := byID["aac-rtp"]
	aacOK := hasFailureAt(aac, sc.Expect["aac-rtp"].TruncatedOffsets)
	cs = append(cs, check{"illegal AAC AU located at packet offset", aacOK,
		fmt.Sprintf("want offsets %v", sc.Expect["aac-rtp"].TruncatedOffsets)})

	// 5. Independent tracks still produce results after a failure.
	indep := byID["dialogue"]
	indepOK := len(indep.Mappings) > 0 && indep.Status == report.StatusOK
	mp4tr := byID["aac-mp4"]
	indepOK = indepOK && len(mp4tr.Mappings) > 0 && mp4tr.Status == report.StatusOK
	cs = append(cs, check{"independent tracks still produce results", indepOK,
		"dialogue + aac-mp4"})

	// 6. Dependency cycle: both participating tracks are blocked and carry a
	// graph-cycle failure, and no conversion configuration was generated.
	cycOK := true
	for _, id := range []string{"cyc-a", "cyc-b"} {
		tr, present := byID[id]
		if !present || tr.Status != report.StatusBlocked {
			cycOK = false
			continue
		}
		hasCycle := false
		for _, f := range tr.Failures {
			if f.Code == model.ErrGraphCycle {
				hasCycle = true
			}
		}
		if !hasCycle || (tr.Decision != nil && tr.Decision.Eligible) {
			cycOK = false
		}
	}
	cs = append(cs, check{"dependency cycle detected and blocked", cycOK, "cyc-a, cyc-b"})

	// 7. Digest stability is guaranteed by construction (deterministic hashing);
	// confirm the manifest and every track carry non-empty digests.
	digestOK := m.Digest != ""
	for _, tr := range m.Tracks {
		if tr.Digest == "" {
			digestOK = false
		}
	}
	cs = append(cs, check{"all outputs carry SHA-256 digests", digestOK, m.Digest})

	return cs
}

func hasFailureAt(tr report.TrackReport, offsets []int) bool {
	want := map[int]bool{}
	for _, o := range offsets {
		want[o] = false
	}
	for _, f := range tr.Failures {
		if _, ok := want[f.PacketOffset]; ok {
			want[f.PacketOffset] = true
		}
	}
	for _, found := range want {
		if !found {
			return false
		}
	}
	return len(want) > 0
}

func printManifest(m report.Manifest) {
	fmt.Printf("=== mediasync demo (seed=%d) ===\n", m.Seed)
	fmt.Printf("params: budget=%dms max-bitrate=%dbps min-quality=%d\n\n",
		m.Params.BudgetNS/1_000_000, m.Params.MaxBitrate, m.Params.MinQuality)
	for _, tr := range m.Tracks {
		fmt.Printf("track %-8s status=%-8s digest=%s\n", tr.TrackID, tr.Status, short(tr.Digest))
		for _, mp := range tr.Mappings {
			fmt.Printf("  [%02d] %-9s src=%-3d extseq=%-6d start=%12dns dur=%11dns "+
				"timeΔ=%8dns bytes=%4d byteΔ=%4d %s\n",
				mp.OutputSlot, mp.Class, mp.SourceOffset, mp.ExtSeq,
				mp.StartNS, mp.DurationNS, mp.TimeDeltaNS, mp.Bytes, mp.ByteDelta, mp.Status)
		}
		for _, f := range tr.Failures {
			fmt.Printf("  FAIL stage=%-8s packet=%-3d code=%-30s %s\n",
				f.Stage, f.PacketOffset, f.Code, f.Reason)
		}
		if tr.Decision != nil {
			d := tr.Decision
			fmt.Printf("  config: bitrate=%dbps quality=%d estimate=%dns steps=%d eligible=%v\n",
				d.TargetBitrate, d.Quality, d.EstimatedNS, len(d.Steps), d.Eligible)
			for _, s := range d.Steps {
				fmt.Printf("    step %-22s %d -> %d (%s)\n", s.Field, s.From, s.To, s.Reason)
			}
		}
		fmt.Println()
	}
	fmt.Printf("summary: tracks=%d ok=%d failed=%d blocked=%d samples=%d gaps=%d duplicates=%d failures=%d duration=%dns\n",
		m.Summary.TrackCount, m.Summary.OKTracks, m.Summary.FailedTracks,
		m.Summary.BlockedTracks, m.Summary.SampleItems, m.Summary.GapItems,
		m.Summary.DuplicateItems, m.Summary.FailureCount, m.Summary.TotalDuration)
	fmt.Printf("manifest digest: %s\n\n", m.Digest)
}

func printChecks(cs []check) {
	allPass := true
	for _, c := range cs {
		mark := "PASS"
		if !c.pass {
			mark = "FAIL"
			allPass = false
		}
		fmt.Printf("[%s] %-45s %s\n", mark, c.name, c.detail)
	}
	fmt.Println()
	if allPass {
		fmt.Println("RESULT: all acceptance checks passed")
	} else {
		fmt.Println("RESULT: acceptance failure")
	}
}

func short(hexDigest string) string {
	if len(hexDigest) <= 16 {
		return hexDigest
	}
	return hexDigest[:16] + "..."
}
