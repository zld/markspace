package rollback

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"mediarepair/internal/cache"
	"mediarepair/internal/fsx"
	"mediarepair/internal/index"
	"mediarepair/internal/media"
)

const PackageSchemaVersion = 1

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type PackageManifest struct {
	SchemaVersion    int        `json:"schema_version"`
	Platform         string     `json:"platform"`
	Version          string     `json:"version"`
	InputFingerprint string     `json:"input_fingerprint"`
	IndexSHA256      string     `json:"index_sha256"`
	IndexVersion     int        `json:"index_version"`
	PackageSHA256    string     `json:"package_sha256"`
	Artifacts        []Artifact `json:"artifacts"`
}

type CurrentPointer struct {
	Version         string `json:"version"`
	PackageSHA256   string `json:"package_sha256"`
	IndexSHA256     string `json:"index_sha256"`
	PreviousVersion string `json:"previous_version,omitempty"`
}

type PublishOptions struct {
	Root             string
	ArtifactsRoot    string
	Platform         string
	Version          string
	InputFingerprint string
	Index            *index.Index
	IndexSHA256      string
}

func platformRoot(root, platform string) string {
	return filepath.Join(root, "packages", platform)
}

func versionDir(root, platform, version string) string {
	return filepath.Join(platformRoot(root, platform), "versions", version)
}

func currentPath(root, platform string) string {
	return filepath.Join(platformRoot(root, platform), "current.json")
}

func manifestPath(dir string) string {
	return filepath.Join(dir, "package-manifest.json")
}

func Publish(opts PublishOptions) (*PackageManifest, error) {
	if opts.Platform == "" || opts.Version == "" || opts.Index == nil {
		return nil, media.E(media.CodeInvalidArgument, "package.publish_options", nil)
	}
	pRoot := platformRoot(opts.Root, opts.Platform)
	versionsRoot := filepath.Join(pRoot, "versions")
	if err := os.MkdirAll(versionsRoot, 0o755); err != nil {
		return nil, media.E(media.CodeWrite, "package.mkdir_versions", err)
	}
	stage, err := os.MkdirTemp(versionsRoot, ".staging-*")
	if err != nil {
		return nil, media.E(media.CodeWrite, "package.stage_dir", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stage)
		}
	}()

	indexStagePath := filepath.Join(stage, "index.json")
	if err := opts.Index.Save(indexStagePath); err != nil {
		return nil, err
	}
	indexFileSHA, err := cache.FileSHA256(indexStagePath)
	if err != nil {
		return nil, media.E(media.CodeWrite, "package.hash_index_file", err)
	}
	indexInfo, err := os.Stat(indexStagePath)
	if err != nil {
		return nil, media.E(media.CodeWrite, "package.stat_index", err)
	}
	artifacts := []Artifact{{Path: "index.json", SHA256: indexFileSHA, Size: indexInfo.Size()}}
	for _, block := range opts.Index.Blocks {
		name := segmentName(block.Index)
		src := filepath.Join(opts.ArtifactsRoot, "segments", name)
		dst := filepath.Join(stage, "segments", name)
		art, err := copyArtifact(src, dst, filepath.ToSlash(filepath.Join("segments", name)))
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, art)
	}

	pm := &PackageManifest{
		SchemaVersion:    PackageSchemaVersion,
		Platform:         opts.Platform,
		Version:          opts.Version,
		InputFingerprint: opts.InputFingerprint,
		IndexSHA256:      opts.IndexSHA256,
		IndexVersion:     opts.Index.Version,
		Artifacts:        artifacts,
	}
	pkgHash, err := hashPackage(pm)
	if err != nil {
		return nil, err
	}
	pm.PackageSHA256 = pkgHash
	if err := saveManifest(stage, pm); err != nil {
		return nil, err
	}
	if _, err := VerifyPackage(stage); err != nil {
		return nil, err
	}

	final := versionDir(opts.Root, opts.Platform, opts.Version)
	if _, err := os.Stat(final); err == nil {
		existing, verr := VerifyPackage(final)
		if verr != nil || existing.PackageSHA256 != pm.PackageSHA256 {
			return nil, media.E(media.CodeVerification, "package.version_exists", verr)
		}
		pm = existing
	} else {
		if err := os.Rename(stage, final); err != nil {
			return nil, media.E(media.CodeWrite, "package.rename_version", err).WithFile(final)
		}
		cleanup = false
		if _, err := VerifyPackage(final); err != nil {
			return nil, err
		}
	}

	old, _ := ReadCurrent(opts.Root, opts.Platform)
	pointer := CurrentPointer{Version: opts.Version, PackageSHA256: pm.PackageSHA256, IndexSHA256: pm.IndexSHA256}
	if old != nil && old.Version != opts.Version {
		pointer.PreviousVersion = old.Version
	}
	if err := writeCurrent(opts.Root, opts.Platform, pointer); err != nil {
		return nil, err
	}
	return pm, nil
}

func copyArtifact(src, dst, rel string) (Artifact, error) {
	in, err := os.Open(src)
	if err != nil {
		return Artifact{}, media.E(media.CodeWrite, "package.open_artifact", err).WithFile(rel)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Artifact{}, media.E(media.CodeWrite, "package.mkdir_artifact", err).WithFile(rel)
	}
	out, err := os.Create(dst)
	if err != nil {
		return Artifact{}, media.E(media.CodeWrite, "package.create_artifact", err).WithFile(rel)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Artifact{}, media.E(media.CodeWrite, "package.copy_artifact", err).WithFile(rel)
	}
	return Artifact{Path: rel, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

func saveManifest(dir string, pm *PackageManifest) error {
	b, err := json.MarshalIndent(pm, "", "  ")
	if err != nil {
		return media.E(media.CodeWrite, "package.encode_manifest", err)
	}
	b = append(b, '\n')
	return fsx.AtomicWriteFile(manifestPath(dir), bytes.NewReader(b), 0o644)
}

func segmentName(block int) string {
	switch {
	case block < 10:
		return "segment-00" + itoa(block) + ".bin"
	case block < 100:
		return "segment-0" + itoa(block) + ".bin"
	default:
		return "segment-" + itoa(block) + ".bin"
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
