package merge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

type mergeError string

func (e mergeError) Error() string { return "merge: " + string(e) }

func errMerge(msg string) error { return mergeError(msg) }

func itoa(v int) string     { return strconv.Itoa(v) }
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// canonicalManifest is the stable, self-contained shape that gets hashed and
// written to disk.  It excludes the volatile commit/rollback bookkeeping so
// the same reconciled content always yields the same digest.
type canonicalManifest struct {
	Version int     `json:"version"`
	Assets  []Asset `json:"assets"`
	Issues  []Issue `json:"issues"`
}

// canonical encodes a manifest deterministically: assets and issues are sorted
// by key and the JSON keys are emitted in struct order, so the byte stream is
// reproducible across runs and machines.
func canonical(m *Manifest) ([]byte, error) {
	assets := make([]Asset, len(m.Assets))
	copy(assets, m.Assets)
	sort.Slice(assets, func(i, j int) bool { return assets[i].Key < assets[j].Key })
	issues := make([]Issue, len(m.Issues))
	copy(issues, m.Issues)
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Key == issues[j].Key {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Key < issues[j].Key
	})
	return json.MarshalIndent(canonicalManifest{
		Version: m.Version,
		Assets:  assets,
		Issues:  issues,
	}, "", "  ")
}

// DigestManifest returns the hex SHA-256 of the canonical manifest encoding.
// Both the stager and the independent validator derive it through this one
// function, so a "pass" means the bytes really match rather than a shared flag.
func DigestManifest(m *Manifest) string {
	b, err := canonical(m)
	if err != nil {
		// canonical only fails on JSON type errors, impossible for these types.
		panic(fmt.Sprintf("merge: canonical encode: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CanonicalBytes exposes the encoded form used by the stager and validator.
func CanonicalBytes(m *Manifest) ([]byte, error) { return canonical(m) }
