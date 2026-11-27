package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
)

// Verify re-checks a manifest from scratch: every derivative file must exist
// with the recorded size and digest, the source digests must be repeated
// consistently, and the recorded counts must match the derivative list. It
// also recomputes the manifest checksum.
func Verify(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(err, fault.StageManifest, fault.KindMissing, "manifest.json", 0, "read manifest")
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fault.Wrap(err, fault.StageManifest, fault.KindCorrupt, "manifest.json", 0, "decode manifest")
	}
	m := &Manifest{
		Version: doc.Version, Sources: doc.Sources, Tracks: doc.Tracks,
		Damage: doc.Damage, TimeRanges: doc.TimeRanges, Derivatives: doc.Derivatives,
		Counts: doc.Counts,
	}
	sortManifest(m)
	normalise(m)
	want, err := m.Checksum()
	if err != nil {
		return nil, err
	}
	if want != doc.Checksum {
		return nil, fault.New(fault.StageManifest, fault.KindDigest, "manifest.json", 0,
			"manifest checksum %s does not match recomputed %s", doc.Checksum, want)
	}
	if err := verifyDerivatives(dir, m); err != nil {
		return nil, err
	}
	if err := verifyCounts(m); err != nil {
		return nil, err
	}
	if err := verifySources(m); err != nil {
		return nil, err
	}
	return m, nil
}

func verifyDerivatives(dir string, m *Manifest) error {
	byDigest := map[string]string{}
	for i, d := range m.Derivatives {
		if len(d.Digest) != 64 {
			return fault.New(fault.StageManifest, fault.KindDigest, "manifest.json", int64(i), "derivative %q has invalid digest %q", d.Kind, d.Digest)
		}
		if d.Key != Key(d.Kind, d.Layer, d.Deps) {
			return fault.New(fault.StageManifest, fault.KindDigest, d.Path, int64(i), "derivative %q key does not match its dependencies", d.Kind)
		}
		if prev, ok := byDigest[d.Digest]; ok {
			if prev != d.Path {
				return fault.New(fault.StageManifest, fault.KindConflict, d.Path, int64(i), "digest %s stored at two paths", d.Digest)
			}
			continue
		}
		byDigest[d.Digest] = d.Path
		full, err := safeJoin(dir, d.Path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return fault.Wrap(err, fault.StageManifest, fault.KindMissing, d.Path, 0, "read derivative")
		}
		if int64(len(content)) != d.Size {
			return fault.New(fault.StageManifest, fault.KindDigest, d.Path, 0, "derivative size %d does not match recorded %d", len(content), d.Size)
		}
		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != d.Digest {
			return fault.New(fault.StageManifest, fault.KindDigest, d.Path, 0, "derivative digest %s does not match recorded %s", got, d.Digest)
		}
	}
	return nil
}

func verifyCounts(m *Manifest) error {
	total := m.Counts.Added + m.Counts.Reused + m.Counts.Changed
	if total != len(m.Derivatives) {
		return fault.New(fault.StageManifest, fault.KindConflict, "manifest.json", 0,
			"counts add up to %d but the manifest lists %d derivatives", total, len(m.Derivatives))
	}
	for name, n := range map[string]int{"added": m.Counts.Added, "reused": m.Counts.Reused, "changed": m.Counts.Changed, "invalidated": m.Counts.Invalidated} {
		if n < 0 {
			return fault.New(fault.StageManifest, fault.KindConflict, "manifest.json", 0, "negative %s count %d", name, n)
		}
	}
	return nil
}

func verifySources(m *Manifest) error {
	seen := map[string]bool{}
	for _, s := range m.Sources {
		if len(s.Digest) != 64 {
			return fault.New(fault.StageManifest, fault.KindDigest, s.Name, 0, "source digest is not SHA-256")
		}
		if seen[s.Digest] {
			return fault.New(fault.StageManifest, fault.KindConflict, s.Name, 0, "source digest %s listed twice", s.Digest)
		}
		seen[s.Digest] = true
		if s.Size < 0 {
			return fault.New(fault.StageManifest, fault.KindInvalid, s.Name, 0, "negative source size")
		}
	}
	return nil
}

// safeJoin resolves p under dir, refusing any path that would escape it.
func safeJoin(dir, p string) (string, error) {
	if filepath.IsAbs(p) {
		return "", fault.New(fault.StageManifest, fault.KindBoundary, p, 0, "absolute derivative path")
	}
	full := filepath.Join(dir, filepath.FromSlash(p))
	if !asset.Within(dir, full) {
		return "", fault.New(fault.StageManifest, fault.KindBoundary, p, 0, "derivative path escapes the output root")
	}
	return full, nil
}
