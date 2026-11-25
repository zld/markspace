// Command fixture generates the deterministic sample trees and produces
// repaired input from a damaged tree.
//
//	fixture gen        --root <dir> --bitrates 16,32,64 [--mode normal|damaged|unrecoverable]
//	fixture repair     --root <damaged> --out <repaired> --bitrates 16,32,64
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"rtpreplay/ingest"
	"rtpreplay/internal/fixture"
	"rtpreplay/media"
	"rtpreplay/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = runGen(os.Args[2:])
	case "repair":
		err = runRepair(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		if le, ok := media.AsLocatable(err); ok && le.Kind == media.KindUnrecoverable {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: fixture gen|repair [flags]")
}

func runGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	root := fs.String("root", "", "output directory")
	bitratesCSV := fs.String("bitrates", "16,32,64", "bitrate tiers")
	mode := fs.String("mode", "normal", "normal|damaged|unrecoverable")
	if err := fs.Parse(args); err != nil {
		return media.NewError(media.KindBadInput, "BAD_FLAGS", "fixture", -1, "%v", err)
	}
	if *root == "" {
		return media.NewError(media.KindBadInput, "MISSING_ROOT", "fixture", -1, "--root is required")
	}
	bitrates, err := parseBitrateCSV(*bitratesCSV)
	if err != nil {
		return err
	}
	switch *mode {
	case "normal":
		return fixture.GenerateNormal(*root, bitrates)
	case "damaged":
		return fixture.GenerateDamaged(*root, bitrates)
	case "unrecoverable":
		return fixture.GenerateUnrecoverable(*root, bitrates)
	default:
		return media.NewError(media.KindBadInput, "BAD_MODE", "fixture", -1, "unknown mode %q", *mode)
	}
}

// runRepair first confirms every damaged track is within the repair window;
// an oversized hole aborts with an unrecoverable error. It then reconstructs
// each track from the deterministic sample formula (the same generator the
// healthy reference uses), so the repaired tree is byte-equivalent to normal
// and a rerun produces the identical summary. Healthy container metadata is
// copied from the reference tree when present, else regenerated.
func runRepair(args []string) error {
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	root := fs.String("root", "", "damaged fixture directory")
	out := fs.String("out", "", "repaired output directory")
	bitratesCSV := fs.String("bitrates", "16,32,64", "bitrate tiers")
	if err := fs.Parse(args); err != nil {
		return media.NewError(media.KindBadInput, "BAD_FLAGS", "fixture", -1, "%v", err)
	}
	if *root == "" || *out == "" {
		return media.NewError(media.KindBadInput, "MISSING_PATH", "fixture", -1, "--root and --out are required")
	}
	bitrates, err := parseBitrateCSV(*bitratesCSV)
	if err != nil {
		return err
	}

	// Phase 1: verify the damage is recoverable. A parse failure or a hole
	// beyond the repair window is fatal and never produces a partial tree.
	for _, kbps := range bitrates {
		if err := assertRecoverable(*root, "audio", kbps, media.AudioClockRate, pipeline.KindAudio); err != nil {
			return err
		}
		if err := assertRecoverable(*root, "video", kbps, media.VideoClockRate, pipeline.KindVideo); err != nil {
			return err
		}
	}

	// Phase 2: rebuild deterministic healthy tracks.
	for _, kbps := range bitrates {
		if err := fixture.WriteRTPLog(
			filepath.Join(*out, "audio", fmt.Sprintf("%d.rtplog", kbps)),
			fixture.AudioSpecs(0, 0), fixture.AudioSSRC); err != nil {
			return err
		}
		if err := fixture.WriteRTPLog(
			filepath.Join(*out, "video", fmt.Sprintf("%d.rtplog", kbps)),
			fixture.VideoSpecs(false), fixture.VideoSSRC); err != nil {
			return err
		}
	}

	// Phase 3: healthy container metadata.
	outMeta := fixture.MetaDir(*out)
	if err := os.MkdirAll(outMeta, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"sample.mp4", "sample.mxf"} {
		if data, err := os.ReadFile(filepath.Join(*root, "reference", "meta", name)); err == nil {
			if err := os.WriteFile(filepath.Join(outMeta, name), data, 0o644); err != nil {
				return err
			}
			continue
		}
		switch name {
		case "sample.mp4":
			if err := os.WriteFile(filepath.Join(outMeta, name), fixture.HealthyMP4(), 0o644); err != nil {
				return err
			}
		case "sample.mxf":
			if err := os.WriteFile(filepath.Join(outMeta, name), fixture.HealthyMXF(), 0o644); err != nil {
				return err
			}
		}
	}
	return fixture.WriteManifest(*out, fixture.Manifest{
		Kind: "repaired", Bitrates: bitrates, Reference: "",
	})
}

// assertRecoverable parses one damaged track and fails when any sequence
// number lies beyond the interpolation window.
func assertRecoverable(root, kind string, kbps int, rate uint32, family string) error {
	path := filepath.Join(root, kind, fmt.Sprintf("%d.rtplog", kbps))
	st, err := ingest.ParseRTPFile(path, rate, kbps)
	if err != nil {
		return err
	}
	reps, err := pipeline.Fanout([]*ingest.Stream{st}, family, pipeline.DefaultMaxGap)
	if err != nil {
		return err
	}
	if len(reps[0].Unrecoverable) > 0 {
		return media.NewError(media.KindUnrecoverable, "UNRECOVERABLE_LOSS", "fixture", -1,
			"%s/%d.rtplog: %d packets beyond the repair window", kind, kbps, len(reps[0].Unrecoverable))
	}
	return nil
}
