package release

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"releasepipe/internal/errs"
	"releasepipe/internal/index"
)

// Version is one committed release: its manifest digest and completeness.
type Version struct {
	Number      int    `json:"number"`
	ManifestSHA string `json:"manifest_sha256"`
	Complete    bool   `json:"complete"`
	Manifest    string `json:"manifest"`
}

func releasesDir(dir string) string { return filepath.Join(dir, "releases") }

func versionPath(dir string, n int) string {
	return filepath.Join(releasesDir(dir), fmt.Sprintf("v%06d.json", n))
}

// CommitVersion appends a complete version and makes it current.
func CommitVersion(dir string, manifest []byte, sha string) (Version, error) {
	n := 1
	entries, err := os.ReadDir(releasesDir(dir))
	if err != nil && !os.IsNotExist(err) {
		return Version{}, errs.Wrap(errs.StageRelease, errs.CodeInternal, dir, -1, err)
	}
	for _, e := range entries {
		var v int
		if _, err := fmt.Sscanf(e.Name(), "v%d.json", &v); err == nil && v >= n {
			n = v + 1
		}
	}
	v := Version{Number: n, ManifestSHA: sha, Complete: true, Manifest: string(manifest)}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return Version{}, errs.Wrap(errs.StageRelease, errs.CodeInternal, dir, -1, err)
	}
	if err := os.MkdirAll(releasesDir(dir), 0o755); err != nil {
		return Version{}, errs.Wrap(errs.StageRelease, errs.CodeInternal, dir, -1, err)
	}
	if err := os.WriteFile(versionPath(dir, n), raw, 0o644); err != nil {
		return Version{}, errs.Wrap(errs.StageRelease, errs.CodeInternal, dir, -1, err)
	}
	cur := fmt.Sprintf("%d\n", n)
	if err := os.WriteFile(filepath.Join(releasesDir(dir), "current"), []byte(cur), 0o644); err != nil {
		return Version{}, errs.Wrap(errs.StageRelease, errs.CodeInternal, dir, -1, err)
	}
	return v, nil
}

// CurrentVersion returns the current version number (0 when none).
func CurrentVersion(dir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(releasesDir(dir), "current"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, errs.Wrap(errs.StageRelease, errs.CodeInternal, dir, -1, err)
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &n); err != nil {
		return 0, errs.New(errs.StageRelease, errs.CodeChecksum, dir, 0,
			"current version pointer corrupt: %v", err)
	}
	return n, nil
}

func loadVersion(dir string, n int) (Version, error) {
	var v Version
	raw, err := os.ReadFile(versionPath(dir, n))
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	return v, nil
}

// eligible reports whether v is a valid rollback target: complete and
// with a manifest that still matches its recorded digest.
func (v Version) eligible() bool {
	return v.Complete && len(v.Manifest) > 0 &&
		index.HashBytes([]byte(v.Manifest)) == v.ManifestSHA
}

// Rollback switches current to the most recent complete, uncorrupted
// version before the current one. Incomplete or damaged versions are
// never eligible targets.
func Rollback(dir string) (Version, error) {
	cur, err := CurrentVersion(dir)
	if err != nil {
		return Version{}, err
	}
	for n := cur - 1; n >= 1; n-- {
		v, err := loadVersion(dir, n)
		if err != nil || !v.eligible() {
			continue
		}
		cur := fmt.Sprintf("%d\n", n)
		if err := os.WriteFile(filepath.Join(releasesDir(dir), "current"),
			[]byte(cur), 0o644); err != nil {
			return Version{}, errs.Wrap(errs.StageRelease, errs.CodeInternal, dir, -1, err)
		}
		return v, nil
	}
	return Version{}, errs.New(errs.StageRelease, errs.CodeNoRollback, "", -1,
		"no complete version before v%d", cur)
}
