package rollback

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"mediarepair/internal/cache"
	"mediarepair/internal/fsx"
	"mediarepair/internal/index"
	"mediarepair/internal/media"
)

func hashPackage(pm *PackageManifest) (string, error) {
	clone := *pm
	clone.PackageSHA256 = ""
	h, err := cache.CanonicalJSONHash(&clone)
	if err != nil {
		return "", media.E(media.CodeVerification, "package.hash", err)
	}
	return h, nil
}

func VerifyPackage(dir string) (*PackageManifest, error) {
	b, err := os.ReadFile(manifestPath(dir))
	if err != nil {
		return nil, media.E(media.CodeWrite, "package.read_manifest", err).WithFile(dir)
	}
	var pm PackageManifest
	if err := json.Unmarshal(b, &pm); err != nil {
		return nil, media.E(media.CodeVerification, "package.parse_manifest", err).WithFile(dir)
	}
	for _, art := range pm.Artifacts {
		full, err := fsx.SafeJoin(dir, art.Path)
		if err != nil {
			return nil, err
		}
		got, err := cache.FileSHA256(full)
		if err != nil {
			return nil, media.E(media.CodeVerification, "package.artifact_read", err).WithFile(art.Path)
		}
		if got != art.SHA256 {
			return nil, media.E(media.CodeVerification, "package.artifact_hash", nil).WithFile(art.Path)
		}
	}
	wantPkg, err := hashPackage(&pm)
	if err != nil {
		return nil, err
	}
	if wantPkg != pm.PackageSHA256 {
		return nil, media.E(media.CodeVerification, "package.hash_mismatch", nil).WithFile(dir)
	}
	idx, err := index.Load(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	idxHash, err := idx.Hash()
	if err != nil {
		return nil, err
	}
	if idxHash != pm.IndexSHA256 {
		return nil, media.E(media.CodeVerification, "package.index_hash", nil).WithFile("index.json")
	}
	return &pm, nil
}

func ReadCurrent(root, platform string) (*CurrentPointer, error) {
	b, err := os.ReadFile(currentPath(root, platform))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, media.E(media.CodeWrite, "current.read", err).WithFile(platform)
	}
	var p CurrentPointer
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, media.E(media.CodeVerification, "current.parse", err).WithFile(platform)
	}
	return &p, nil
}

func writeCurrent(root, platform string, p CurrentPointer) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return media.E(media.CodeWrite, "current.encode", err)
	}
	b = append(b, '\n')
	return fsx.AtomicWriteFile(currentPath(root, platform), bytes.NewReader(b), 0o644)
}

// Rollback moves the current pointer to the previous version only after the
// previous package has been fully rehashed and index-verified.
func Rollback(root, platform string) (*PackageManifest, error) {
	cur, err := ReadCurrent(root, platform)
	if err != nil {
		return nil, err
	}
	if cur == nil || cur.PreviousVersion == "" {
		return nil, media.E(media.CodeInvalidArgument, "rollback.no_previous", nil).WithFile(platform)
	}
	prevDir := versionDir(root, platform, cur.PreviousVersion)
	pm, err := VerifyPackage(prevDir)
	if err != nil {
		return nil, err
	}
	next := CurrentPointer{
		Version:       pm.Version,
		PackageSHA256: pm.PackageSHA256,
		IndexSHA256:   pm.IndexSHA256,
	}
	if err := writeCurrent(root, platform, next); err != nil {
		return nil, err
	}
	return pm, nil
}
