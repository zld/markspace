// Package batch splits a timeline into bounded sample chunks and coordinates
// index commits, remuxing and restart recovery.
package batch

import (
	"path/filepath"

	"mediapack/internal/fault"
	"mediapack/internal/index"
	"mediapack/internal/maptrack"
	"mediapack/internal/timeline"
)

const ConfigFileName = "project.json"

type Config struct {
	OutDir           string          `json:"out_dir"`
	ChunkSamples     int             `json:"chunk_samples"`
	TargetBitrateBps float64         `json:"target_bitrate_bps"`
	MP4Inputs        []string        `json:"mp4_inputs"`
	RTPInputs        []string        `json:"rtp_inputs"`
	RTPClockRate     uint32          `json:"rtp_clock_rate"`
	Rules            []maptrack.Rule `json:"rules"`
	Timeline         timeline.Spec   `json:"timeline"`
}

type Result struct {
	Manifest      *index.Manifest `json:"manifest"`
	Unrecoverable []*fault.Error  `json:"unrecoverable,omitempty"`
}

func SaveConfig(cfg Config) (string, error) {
	return index.SaveJSON(cfg.OutDir, ConfigFileName, cfg)
}

func LoadConfig(dir string) (Config, error) {
	var cfg Config
	p, err := safeConfigPath(dir)
	if err != nil {
		return cfg, err
	}
	if err := index.LoadJSON(p, &cfg); err != nil {
		return cfg, err
	}
	if cfg.OutDir == "" {
		cfg.OutDir = dir
	}
	return cfg, nil
}

func safeConfigPath(dir string) (string, error) {
	// index.LoadJSON accepts an absolute path; keep the fixed file name local.
	return filepath.Join(dir, ConfigFileName), nil
}
