// Package fixture generates reproducible local media byte samples and the
// fault-injection cases used to exercise key rotation, license validation
// and authenticated decryption. It never reads samples from outside the
// repository: every byte is derived from a fixed seed.
package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"sort"
	"time"

	"mediakey/internal/model"
)

// ShardBlockSize is the per-shard size upper limit ("one block"). Samples
// deliberately include an empty shard, a shard exactly at the limit and a
// final shard shorter than one block.
const ShardBlockSize = 4096

// Fault kinds injected by fixtures. They mirror the failure reasons in
// package model but are declared separately so that fixtures stay decoupled
// from validation internals.
const (
	FaultNone           = "NONE"
	FaultKeyExpired     = model.ReasonKeyExpired
	FaultLicenseExpired = model.ReasonLicenseExpired
	FaultLicenseReplay  = model.ReasonLicenseReplay
	FaultAuthTag        = model.ReasonAuthTag
	FaultManifest       = model.ReasonManifest
)

// Sample bundles a generated manifest, the plaintext bytes keyed by shard id
// and the per-shard fault injection plan.
type Sample struct {
	Manifest   model.Manifest
	Plaintexts map[int][]byte
	Faults     map[int]string
}

// Mixed scenario fault positions. With 12 shards and rotation every 4 these
// land in four different key groups, producing 4 rejections / 8 accepts.
const (
	MixedKeyExpiredShard     = 2
	MixedLicenseExpiredShard = 5
	MixedReplayShard         = 8
	MixedAuthTagShard        = 11
)

// ManifestDigest computes the SHA-256 digest over a canonical JSON rendering
// of the manifest. The digest field itself is excluded.
func ManifestDigest(m model.Manifest) string {
	return digestManifest(m)
}

// digestManifest computes the SHA-256 digest over a canonical JSON rendering
// of the manifest. The digest field itself is excluded.
func digestManifest(m model.Manifest) string {
	cp := m
	cp.Digest = ""
	raw, err := json.Marshal(cp)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// shardSize returns the deterministic plaintext size for shard i out of n.
func shardSize(i, n int) int {
	switch {
	case i == 0:
		return 0 // empty shard
	case i == 1:
		return ShardBlockSize // exactly at the per-shard limit
	case i == n-1:
		return ShardBlockSize - 37 // trailing shard shorter than one block
	default:
		// Deterministic interior size, strictly between 0 and the limit.
		return 1024 + (i*337)%(ShardBlockSize-1024-1)
	}
}

// generateBytes fills b with deterministic bytes derived from seed and
// shard id. A zero-length slice stays empty.
func generateBytes(b []byte, seed, shardID int) {
	rng := rand.New(rand.NewSource(int64(seed)*1000 + int64(shardID)))
	// Read never errors for the library rand source.
	_, _ = rng.Read(b)
}

// hashBytes returns the hex SHA-256 of b, including the empty input case.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BuildSample deterministically generates n media shards and their manifest.
// No fault is applied; use ApplyScenario or SetFault afterwards.
func BuildSample(packageID string, n, seed int) *Sample {
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	m := model.Manifest{
		PackageID:   packageID,
		ShardSize:   ShardBlockSize,
		GeneratedAt: base.UTC().Format(time.RFC3339),
	}
	plain := make(map[int][]byte, n)
	for i := 0; i < n; i++ {
		size := shardSize(i, n)
		buf := make([]byte, size)
		generateBytes(buf, seed, i)
		plain[i] = buf
		m.Shards = append(m.Shards, model.Shard{
			ShardID:       i,
			Size:          size,
			PlaintextHash: hashBytes(buf),
		})
	}
	m.Digest = digestManifest(m)
	return &Sample{
		Manifest:   m,
		Plaintexts: plain,
		Faults:     map[int]string{},
	}
}

// SetFault records a fault injection for one shard.
func (s *Sample) SetFault(shardID int, fault string) {
	s.Faults[shardID] = fault
}

// Fault returns the fault for a shard, or FaultNone.
func (s *Sample) Fault(shardID int) string {
	if f, ok := s.Faults[shardID]; ok {
		return f
	}
	return FaultNone
}

// SortedFaultShards returns fault-plan shard ids in ascending order.
func (s *Sample) SortedFaultShards() []int {
	ids := make([]int, 0, len(s.Faults))
	for id := range s.Faults {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// ApplyScenario configures the fault plan for a named demo scenario:
//   - "ok":     no injected faults, every shard is accepted;
//   - "mixed":  one each of key expiry, license expiry, replay and
//     authentication-tag corruption (4 rejects);
//   - "chaos":  all five fault kinds across distinct shards.
func (s *Sample) ApplyScenario(scenario string) {
	n := len(s.Manifest.Shards)
	switch scenario {
	case "ok":
		return
	case "mixed":
		s.SetFault(MixedKeyExpiredShard, FaultKeyExpired)
		s.SetFault(MixedLicenseExpiredShard, FaultLicenseExpired)
		s.SetFault(MixedReplayShard, FaultLicenseReplay)
		s.SetFault(MixedAuthTagShard, FaultAuthTag)
	case "chaos":
		s.SetFault(1, FaultKeyExpired)
		s.SetFault(3, FaultLicenseExpired)
		s.SetFault(5, FaultLicenseReplay)
		s.SetFault(7, FaultAuthTag)
		if n > 9 {
			s.SetFault(9, FaultManifest)
		}
		if n > 11 {
			s.SetFault(11, FaultLicenseReplay)
		}
	}
}
