package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// manifest is the cross-run bookkeeping the verify subcommand reads. It lists
// each generated asset and the demo counters, independent of any sidecar.
type manifest struct {
	Assets []manifestAsset `json:"assets"`
}

type manifestAsset struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	Recomputed int    `json:"recomputed"`
	Reused     int    `json:"reused"`
	PSIVersions int   `json:"psi_versions"`
	CCGaps      int   `json:"cc_gaps"`
	TruncBytes  int   `json:"trunc_bytes"`
}

func manifestPath(dir string) string { return filepath.Join(dir, "manifest.json") }

func writeManifest(dir string, runs []*assetRun) error {
	var m manifest
	for _, r := range runs {
		m.Assets = append(m.Assets, manifestAsset{
			Name: r.name, Source: filepath.Base(r.sourcePath),
			Recomputed: r.recomputed, Reused: r.reused,
			PSIVersions: r.psiVersions, CCGaps: r.ccGaps, TruncBytes: r.truncBytes,
		})
	}
	data, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, manifestPath(dir))
}

func readManifest(dir string) (*manifest, error) {
	data, err := os.ReadFile(manifestPath(dir))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
