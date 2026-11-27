// Package manifest reads and writes the release resource manifests.
//
// v1 is the legacy inventory shipped by earlier tooling.  It is only ever
// read through the version adapter; nothing else in the pipeline may parse
// it.  v2 is the versioned, digest-protected manifest produced by this tool.
package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"example.com/assetpack/internal/fault"
)

// V1 is the legacy manifest.
type V1 struct {
	Version int       `json:"version"`
	Release string    `json:"release"`
	Assets  []V1Asset `json:"assets"`
}

// V1Asset is one legacy resource record.
type V1Asset struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Rate       int    `json:"rate"`
	Channels   int    `json:"channels"`
	DurationMS int64  `json:"duration_ms"`
	SHA256     string `json:"sha256"`
}

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ReadV1 parses and structurally validates a legacy manifest.  Unknown
// versions and missing required fields are rejected with structured faults.
func ReadV1(r io.Reader) (*V1, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindIO, "read legacy manifest: %v", err)
	}
	var probe struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindMissing,
			"legacy manifest is not valid JSON: %v", err)
	}
	if probe.Version == nil {
		return nil, fault.New(fault.StageManifest, fault.KindMissing, "legacy manifest has no version field")
	}
	if *probe.Version != 1 {
		return nil, fault.New(fault.StageManifest, fault.KindUnknownVer,
			"legacy manifest version %d is not supported by the v1 reader", *probe.Version)
	}
	var m V1
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fault.New(fault.StageManifest, fault.KindMissing,
			"legacy manifest shape invalid: %v", err)
	}
	if m.Release == "" {
		return nil, fault.New(fault.StageManifest, fault.KindMissing, "legacy manifest has no release name")
	}
	if len(m.Assets) == 0 {
		return nil, fault.New(fault.StageManifest, fault.KindMissing, "legacy manifest has no assets")
	}
	seen := map[string]bool{}
	for i, a := range m.Assets {
		if a.Name == "" {
			return nil, fault.New(fault.StageManifest, fault.KindMissing, "asset %d has no name", i).
				AtSample(int64(i)).Recover(false)
		}
		if seen[a.Name] {
			return nil, fault.New(fault.StageManifest, fault.KindInvalidArg,
				"asset %q appears twice in the legacy manifest", a.Name).AtSample(int64(i))
		}
		seen[a.Name] = true
		if a.Path == "" {
			return nil, fault.New(fault.StageManifest, fault.KindMissing,
				"asset %q has no source path", a.Name).AtSample(int64(i))
		}
		if a.Rate <= 0 || a.Channels <= 0 || a.DurationMS <= 0 {
			return nil, fault.New(fault.StageManifest, fault.KindMissing,
				"asset %q has an incomplete audio description (rate=%d channels=%d duration_ms=%d)",
				a.Name, a.Rate, a.Channels, a.DurationMS).AtSample(int64(i))
		}
		if !hexDigest.MatchString(a.SHA256) {
			return nil, fault.New(fault.StageManifest, fault.KindMissing,
				"asset %q has a malformed sha256 %q", a.Name, a.SHA256).AtSample(int64(i))
		}
	}
	return &m, nil
}

// Summary is a stable one-line description used in terminal output.
func (m *V1) Summary() string {
	return fmt.Sprintf("v1 release=%s assets=%d", m.Release, len(m.Assets))
}
