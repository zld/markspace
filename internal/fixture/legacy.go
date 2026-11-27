// Package fixture: legacy manifest fixtures.
package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"example.com/assetpack/internal/fault"
)

// --- legacy manifest fixtures ---

// LegacyAsset describes one v1 asset entry.
type LegacyAsset struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Rate       int    `json:"rate"`
	Channels   int    `json:"channels"`
	DurationMS int64  `json:"duration_ms"`
	SHA256     string `json:"sha256"`
}

// LegacyManifest is the v1 fixture.
type LegacyManifest struct {
	Version int           `json:"version"`
	Release string        `json:"release"`
	Assets  []LegacyAsset `json:"assets"`
}

// DefaultAssets is the fixed asset set used by the demo.
func DefaultAssets() []AssetSpec {
	return []AssetSpec{
		{Name: "intro", Rate: 44100, Channels: 2, Freq: 330},
		{Name: "bed", Rate: 48000, Channels: 1, Freq: 220},
		{Name: "sting", Rate: 32000, Channels: 2, Freq: 880},
	}
}

// TSFiles maps each asset to the transport stream bytes the parser will read.
func TSFiles(specs []AssetSpec) map[string][]byte {
	out := make(map[string][]byte, len(specs))
	for i, s := range specs {
		opts := StreamOptions{Asset: s, PID: uint16(0x102 + i), PCRPID: uint16(0x201 + i),
			PMTPID: uint16(0x1000 + i), Program: uint16(i + 1)}
		out[s.Name+".ts"] = BuildTS(opts)
	}
	return out
}

// LegacyFor builds a legacy manifest whose digests are the SHA-256 of the
// generated transport streams.
func LegacyFor(release string, specs []AssetSpec, files map[string][]byte) *LegacyManifest {
	m := &LegacyManifest{Version: 1, Release: release}
	for _, s := range specs {
		key := s.Name + ".ts"
		sum := sha256.Sum256(files[key])
		frames := FramesFor(s)
		m.Assets = append(m.Assets, LegacyAsset{
			Name: s.Name, Path: key, Rate: s.Rate, Channels: s.Channels,
			DurationMS: int64(frames) * 1000 / int64(s.Rate),
			SHA256:     hex.EncodeToString(sum[:]),
		})
	}
	return m
}

// Encode renders the legacy manifest as JSON.
func (m *LegacyManifest) Encode() ([]byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fault.New(fault.StageFixture, fault.KindIO, "encode legacy manifest: %v", err)
	}
	return append(raw, '\n'), nil
}

// StableNameOrders sorts asset names for deterministic iteration.
func StableNameOrders(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Description returns a stable summary of the generated set.
func Description(specs []AssetSpec) string {
	parts := make([]string, 0, len(specs))
	for _, s := range specs {
		parts = append(parts, fmt.Sprintf("%s:%d/%d", s.Name, s.Rate, s.Channels))
	}
	return strings.Join(parts, " ")
}
