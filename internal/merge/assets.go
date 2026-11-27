// Package merge maps and reconciles several versions of the same media
// assets.  Versions are joined on a stable asset key plus a time range; the
// reconciler reports every missing asset, index conflict and byte conflict
// instead of silently preferring one source.
//
// Output is produced in two phases.  Build only reconciles the versions into a
// manifest in memory; Stager then writes that manifest to a staging area and
// either commits it atomically or rolls it back while keeping the recorded
// failure reason, so an invalid result can never replace the last good one.
package merge

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// Asset is one reconciled media asset in the output manifest.
type Asset struct {
	Key       string         `json:"key"`
	Version   string         `json:"version"`    // winning source version
	UnitIndex int            `json:"unit_index"` // referenced elementary unit
	Offset    int64          `json:"offset"`     // unit byte offset in the stream
	Size      int64          `json:"size"`       // payload byte count
	TimeFrom  clock.Rational `json:"time_from"`  // inclusive start, seconds
	TimeTo    clock.Rational `json:"time_to"`    // inclusive end, seconds
	Digest    string         `json:"digest"`     // hex SHA-256 of the payload bytes
}

// Entry is one version's claim on an asset key.
type Entry struct {
	Asset
	// Payload is the raw bytes the digest was computed over.  It is never
	// serialised into the manifest; the manifest keeps only the digest.
	Payload []byte `json:"-"`
}

// Version is a single source version contributing asset entries.
type Version struct {
	Name    string
	Entries []Entry
}

// Issue records one reconciliation problem with the asset key it concerns.
type Issue struct {
	Code fault.Code
	Key  string
	Msg  string
}

// Manifest is the reconciled, canonical output of a merge.
type Manifest struct {
	Assets    []Asset `json:"assets"`
	Version   int     `json:"version"`
	Issues    []Issue `json:"issues"`
	Digest    string  `json:"digest"`     // SHA-256 of the canonical encoding
	Committed bool    `json:"committed"`  // whether a stager committed it
	RolledBack bool   `json:"rolled_back"`
	FailReason string `json:"fail_reason,omitempty"`
}

const manifestVersion = 1

// Build joins every version on the asset key and reconciles conflicts.
//
// Missing keys (absent from at least one version), index conflicts (the same
// key points at a different unit/offset/time range) and byte conflicts (the
// same reference but different payload bytes) are all reported.  A winner is
// still selected deterministically so downstream validation has concrete
// bytes to inspect: the lexicographically smallest version name wins, and
// within a version the first entry for the key wins.
func Build(versions []Version, report *fault.Report) (*Manifest, error) {
	if report == nil {
		return nil, errMerge("nil fault report")
	}
	if len(versions) == 0 {
		return nil, errMerge("no versions to merge")
	}
	names := make([]string, 0, len(versions))
	byVersion := make(map[string]map[string]Entry, len(versions))
	keySet := make(map[string]struct{})
	for _, v := range versions {
		if v.Name == "" {
			return nil, errMerge("version with empty name")
		}
		names = append(names, v.Name)
		entries := make(map[string]Entry, len(v.Entries))
		for _, e := range v.Entries {
			if e.Key == "" {
				return nil, errMerge("entry with empty asset key in version " + v.Name)
			}
			keySet[e.Key] = struct{}{}
			if _, exists := entries[e.Key]; !exists {
				entries[e.Key] = e
			}
		}
		byVersion[v.Name] = entries
	}
	sort.Strings(names)
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	m := &Manifest{Version: manifestVersion}
	zero := clock.MustRational(0, 1)
	for _, key := range keys {
		var present []string
		for _, name := range names {
			if _, ok := byVersion[name][key]; ok {
				present = append(present, name)
			}
		}
		if len(present) < len(names) {
			msg := "asset present in " + joinNames(present) + " but missing from " +
				joinNames(missingNames(names, present))
			m.Issues = append(m.Issues, Issue{Code: fault.CodeAssetMissing, Key: key, Msg: msg})
			report.Addf(fault.CodeAssetMissing, -1, zero, zero, "%s: %s", key, msg)
		}
		// Winner: smallest version name that has the key.
		winnerName := present[0]
		winner := byVersion[winnerName][key]
		// Compare every other claim against the winner.
		for _, name := range names {
			e, ok := byVersion[name][key]
			if !ok || name == winnerName {
				continue
			}
			if referencesDiffer(winner, e) {
				msg := "version " + name + " references unit " + itoa(e.UnitIndex) +
					" @" + itoa64(e.Offset) + ", " + winnerName + " references unit " +
					itoa(winner.UnitIndex) + " @" + itoa64(winner.Offset)
				m.Issues = append(m.Issues, Issue{Code: fault.CodeIndexConflict, Key: key, Msg: msg})
				report.Addf(fault.CodeIndexConflict, winner.Offset, winner.TimeFrom, winner.TimeTo,
					"%s: %s", key, msg)
				continue
			}
			if digestOf(e.Payload) != digestOf(winner.Payload) {
				msg := "byte content differs between versions " + winnerName + " and " + name
				m.Issues = append(m.Issues, Issue{Code: fault.CodeByteConflict, Key: key, Msg: msg})
				report.Addf(fault.CodeByteConflict, winner.Offset, winner.TimeFrom, winner.TimeTo,
					"%s: %s", key, msg)
			}
		}
		winner.Asset.Digest = digestOf(winner.Payload)
		winner.Asset.Version = winnerName
		m.Assets = append(m.Assets, winner.Asset)
	}
	m.Digest = DigestManifest(m)
	return m, nil
}

// referencesDiffer reports whether two entries point at a different unit,
// offset, size or time range — an index-level rather than byte-level conflict.
func referencesDiffer(a, b Entry) bool {
	return a.UnitIndex != b.UnitIndex || a.Offset != b.Offset || a.Size != b.Size ||
		a.TimeFrom.Cmp(b.TimeFrom) != 0 || a.TimeTo.Cmp(b.TimeTo) != 0
}

func digestOf(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

func missingNames(all, present []string) []string {
	have := make(map[string]struct{}, len(present))
	for _, n := range present {
		have[n] = struct{}{}
	}
	var out []string
	for _, n := range all {
		if _, ok := have[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}
