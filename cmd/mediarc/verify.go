package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zld/mediarc/internal/manifest"
	"github.com/zld/mediarc/internal/model"
	"github.com/zld/mediarc/internal/timeline"
)

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	var input string
	fs.StringVar(&input, "input", "", "demo output directory")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return &model.ConfigError{Field: "flags", Err: err}
	}
	if input == "" {
		return &model.ConfigError{Field: "input", Err: errors.New("required")}
	}

	manifestPath := filepath.Join(input, "manifest.json")
	m, err := manifest.Read(manifestPath)
	if err != nil {
		return err
	}
	if err := manifest.VerifyManifestHash(m); err != nil {
		return err
	}
	matched, total, err := manifest.VerifyChunkFiles(input, m)
	if err != nil {
		return err
	}
	if matched != total || matched != m.Metrics.SegmentHashMatched {
		return &model.IntegrityError{Path: manifestPath, Err: fmt.Errorf("segment hash match %d/%d", matched, total)}
	}

	checkpointPath := filepath.Join(input, "checkpoint.json")
	if cp, err := timeline.LoadCheckpoint(checkpointPath); err == nil {
		assetPath := cp.ResolveInputPath(checkpointPath)
		data, err := os.ReadFile(assetPath)
		if err != nil {
			return &model.IntegrityError{Path: assetPath, Err: err}
		}
		if err := cp.VerifyBytes(data); err != nil {
			return err
		}
		if cp.BaseManifestSHA256 != "" && cp.BaseManifestSHA256 != m.ManifestSHA256 {
			return &model.IntegrityError{
				Path: checkpointPath,
				Err:  fmt.Errorf("checkpoint manifest %s != %s", cp.BaseManifestSHA256, m.ManifestSHA256),
			}
		}
		fmt.Printf("checkpoint_digest=ok\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	fmt.Printf("verification=ok\n")
	fmt.Printf("manifest_sha256=%s\n", m.ManifestSHA256)
	fmt.Printf("roundtrip_rate=%.3f\n", m.Metrics.RoundtripRate)
	fmt.Printf("keyframe_hit_rate=%.3f\n", m.Metrics.KeyframeHitRate)
	fmt.Printf("segment_hash_match=%d/%d\n", matched, total)
	return nil
}
