// Package manifest: idempotent merging of committed and fresh manifests.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"example.com/assetpack/internal/fault"
)

// DiffCounts is the verifiable outcome of a merge or re-run.
type DiffCounts struct {
	Added    int `json:"added"`
	Reused   int `json:"reused"`
	Replaced int `json:"replaced"`
	Removed  int `json:"removed"`
}

// Total is the number of resources in the result.
func (d DiffCounts) Total() int { return d.Added + d.Reused + d.Replaced }

// Applied reports whether the merge changed anything.
func (d DiffCounts) Applied() bool { return d.Added > 0 || d.Replaced > 0 || d.Removed > 0 }

// Merge combines a committed manifest with a freshly built one.  Resources
// are keyed by name: an identical resource is reused, a changed one is
// replaced in place, and nothing is ever appended twice.  A nil committed
// manifest is treated as empty.
func Merge(committed, fresh *V2) (*V2, DiffCounts, error) {
	var counts DiffCounts
	if fresh == nil {
		return nil, counts, fault.New(fault.StageManifest, fault.KindMissing, "nothing to merge")
	}
	out := fresh.Clone()
	if committed == nil {
		counts.Added = len(out.Resources)
		if _, err := out.ComputeDigest(); err != nil {
			return nil, counts, err
		}
		return out, counts, nil
	}
	if committed.Release != "" && fresh.Release != "" && committed.Release != fresh.Release {
		return nil, counts, fault.New(fault.StageManifest, fault.KindInvalidArg,
			"cannot merge releases %q and %q", committed.Release, fresh.Release)
	}
	existing := make(map[string]Resource, len(committed.Resources))
	for _, r := range committed.Resources {
		existing[r.Name] = r
	}
	merged := committed.Clone()
	index := make(map[string]int, len(merged.Resources))
	for i, r := range merged.Resources {
		index[r.Name] = i
	}
	for _, r := range out.Resources {
		old, ok := existing[r.Name]
		switch {
		case !ok:
			counts.Added++
			index[r.Name] = len(merged.Resources)
			merged.Resources = append(merged.Resources, r)
		case equivalent(old, r):
			counts.Reused++
		default:
			counts.Replaced++
			merged.Resources[index[r.Name]] = r
		}
	}
	// Anything present before but absent from the fresh set is dropped only
	// when the fresh set is a complete rebuild of the same release.
	freshNames := make(map[string]bool, len(out.Resources))
	for _, r := range out.Resources {
		freshNames[r.Name] = true
	}
	kept := merged.Resources[:0]
	for _, r := range merged.Resources {
		if freshNames[r.Name] {
			kept = append(kept, r)
		} else {
			counts.Removed++
		}
	}
	merged.Resources = kept
	if counts.Added == 0 && counts.Replaced == 0 && counts.Removed == 0 {
		// Nothing changed: keep the committed bytes and digest untouched.
		return committed, counts, nil
	}
	merged.Sequence = committed.Sequence + 1
	if _, err := merged.ComputeDigest(); err != nil {
		return nil, counts, err
	}
	return merged, counts, nil
}

// equivalent compares the fields that determine the released bytes.
func equivalent(a, b Resource) bool {
	if a.SHA256 != b.SHA256 || a.Bytes != b.Bytes || a.Artifact != b.Artifact ||
		a.Rate != b.Rate || a.Layout != b.Layout || a.Frames != b.Frames ||
		a.SourceSHA256 != b.SourceSHA256 || a.Kind != b.Kind {
		return false
	}
	if len(a.Media) != len(b.Media) {
		return false
	}
	for i := range a.Media {
		if a.Media[i] != b.Media[i] {
			return false
		}
	}
	return true
}

// Verify checks that every resource has a matching file digest, given a
// lookup from artifact name to actual SHA-256.  Missing artifacts and digest
// mismatches are hard faults.
func (m *V2) Verify(digests map[string]string) error {
	d := m.Clone()
	var errs fault.List
	names := d.Names()
	sort.Strings(names)
	for _, name := range names {
		r, _ := d.Lookup(name)
		got, ok := digests[r.Artifact]
		if !ok {
			errs.Add(fault.New(fault.StageVerify, fault.KindMissing,
				"resource %q artifact %q is absent", r.Name, r.Artifact))
			continue
		}
		if got != r.SHA256 {
			errs.Add(fault.New(fault.StageVerify, fault.KindDigest,
				"resource %q artifact digest %s does not match manifest %s", r.Name, got, r.SHA256))
		}
	}
	if errs.Len() > 0 {
		return &errs
	}
	return nil
}

// Summary renders a stable terminal summary line.
func (m *V2) Summary() string {
	if m == nil {
		return "no manifest"
	}
	return fmt.Sprintf("%s release=%s seq=%d resources=%d digest=%s",
		m.Schema, m.Release, m.Sequence, len(m.Resources), m.Digest)
}

// VerifyArtifacts verifies a manifest against the artifact files in dir.
func (a *Adapter) VerifyArtifacts(dir string, m *V2) error {
	digests := make(map[string]string, len(m.Resources))
	for _, r := range m.Resources {
		path := filepath.Join(dir, r.Artifact)
		f, err := os.Open(path)
		if err != nil {
			return fault.New(fault.StageVerify, fault.KindMissing, "artifact %q: %v", r.Artifact, err)
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return fault.New(fault.StageVerify, fault.KindIO, "hash %q: %v", r.Artifact, err)
		}
		if n != r.Bytes {
			return fault.New(fault.StageVerify, fault.KindDigest,
				"artifact %q is %d bytes, manifest says %d", r.Artifact, n, r.Bytes)
		}
		digests[r.Artifact] = hex.EncodeToString(h.Sum(nil))
	}
	return m.Verify(digests)
}
