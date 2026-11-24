package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"medpipe/internal/clock"
	"medpipe/internal/sample"
)

type fixture struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Config      sample.Config `json:"config"`
	SHA256      string        `json:"sha256"`
	WireChunks  int           `json:"wire_chunks"`
}

func main() {
	out := flag.String("out", "testdata/fixtures", "fixture output directory")
	flag.Parse()
	if err := generate(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	configs := []fixture{
		{
			Name: "01-wrap-reorder", Description: "out-of-order RTP packets across uint32 timestamp wrap",
			Config: func() sample.Config {
				c := sample.DefaultConfig()
				c.Seed, c.Packets, c.Loss, c.Reorder, c.Duplicate = 101, 600, 0, 1, 0
				c.CorruptOffset = 0
				return c
			}(),
		},
		{
			Name: "02-missing-rtcp", Description: "RTP source with no RTCP sender reports",
			Config: func() sample.Config {
				c := sample.DefaultConfig()
				c.Seed, c.Packets = 102, 500
				c.SuppressRTCP = true
				c.CorruptOffset = 0
				return c
			}(),
		},
		{
			Name: "03-corrupt-boundary", Description: "corrupted frame near a segment boundary",
			Config: func() sample.Config {
				c := sample.DefaultConfig()
				c.Seed, c.Packets, c.CorruptOffset = 103, 600, 4097
				return c
			}(),
		},
		{
			Name: "04-slow-consumer", Description: "loss, reorder and duplicate stream for bounded queue recovery",
			Config: func() sample.Config {
				c := sample.DefaultConfig()
				c.Seed, c.Packets = 104, 1600
				c.CorruptOffset = 0
				return c
			}(),
		},
	}

	manifest := make([]fixture, 0, len(configs))
	for _, f := range configs {
		gen, err := sample.Generate(f.Config)
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.Name, err)
		}
		dataPath := filepath.Join(out, f.Name+".mdp")
		if err := clock.AtomicWriteFile(dataPath, gen.Data, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(gen.Data)
		f.SHA256 = hex.EncodeToString(sum[:])
		f.WireChunks = gen.WireChunks
		meta, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return err
		}
		meta = append(meta, '\n')
		if err := os.WriteFile(filepath.Join(out, f.Name+".json"), meta, 0o644); err != nil {
			return err
		}
		manifest = append(manifest, f)
		fmt.Printf("%s %s chunks=%d\n", f.SHA256, f.Name, f.WireChunks)
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "fixtures.json"), append(b, '\n'), 0o644)
}
