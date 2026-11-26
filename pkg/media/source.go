package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Digest is a content digest using a named algorithm.
type Digest struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

func (d Digest) String() string { return d.Algorithm + ":" + d.Value }

// SHA256Reader consumes r as a stream and returns its lowercase hex SHA-256.
func SHA256Reader(r io.Reader) (Digest, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return Digest{}, n, err
	}
	return Digest{Algorithm: "sha256", Value: hex.EncodeToString(h.Sum(nil))}, n, nil
}

// SHA256Bytes returns the SHA-256 digest of b.
func SHA256Bytes(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest{Algorithm: "sha256", Value: hex.EncodeToString(sum[:])}
}

// Fingerprint separates the three change domains required by the cache rules.
type Fingerprint struct {
	StreamSHA256    string `json:"stream_sha256"`
	ContainerSHA256 string `json:"container_sha256,omitempty"`
	MetadataSHA256  string `json:"metadata_sha256,omitempty"`
}

// Asset is an immutable source revision. The asset ID is the whole-file
// digest; replacements at the same logical path receive a new ID.
type Asset struct {
	ID          string      `json:"asset_id"`
	LogicalPath string      `json:"logical_path"`
	LocalPath   string      `json:"local_path,omitempty"`
	Size        int64       `json:"size"`
	File        Digest      `json:"file_digest"`
	Fingerprint Fingerprint `json:"fingerprint"`
	Registered  time.Time   `json:"registered_at"`
}

// PathHistory records every immutable asset ever registered at a path.
type PathHistory struct {
	Path     string   `json:"path"`
	Current  string   `json:"current_asset_id"`
	AssetIDs []string `json:"asset_ids"`
}

// Registry stores source blobs, immutable manifests, and path histories.
type Registry struct {
	root string
}

// NewRegistry opens or creates a local asset registry.
func NewRegistry(root string) (*Registry, error) {
	if root == "" {
		return nil, NewError(CatInvalidArgument, "", -1, -1, "registry root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, NewError(CatInvalidArgument, "", -1, -1, "invalid registry root: %v", err)
	}
	for _, d := range []string{abs, filepath.Join(abs, "objects"), filepath.Join(abs, "manifests"), filepath.Join(abs, "paths")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return &Registry{root: abs}, nil
}

// Root returns the registry root.
func (r *Registry) Root() string { return r.root }

// RegisterFile streams a local file, installs an immutable blob/manifest, and
// appends its digest to the logical path's replacement history.
func (r *Registry) RegisterFile(logicalPath, localPath string, fp Fingerprint) (*Asset, error) {
	if logicalPath == "" {
		logicalPath = localPath
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, NewError(CatInvalidArgument, "", -1, -1, "source is not a regular file: %s", localPath)
	}

	f, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	d, n, hashErr := SHA256Reader(f)
	closeErr := f.Close()
	if hashErr != nil {
		return nil, hashErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	asset := &Asset{
		ID:          d.Value,
		LogicalPath: logicalPath,
		LocalPath:   localPath,
		Size:        n,
		File:        d,
		Fingerprint: fp,
		Registered:  time.Now().UTC(),
	}
	if err := r.installBlob(localPath, d.Value); err != nil {
		return nil, err
	}
	if err := r.writeManifest(asset); err != nil {
		return nil, err
	}
	if err := r.appendHistory(logicalPath, d.Value); err != nil {
		return nil, err
	}
	return asset, nil
}

func (r *Registry) objectPath(id string) string {
	return filepath.Join(r.root, "objects", id[:2], id)
}

func (r *Registry) installBlob(src, id string) error {
	dst := r.objectPath(id)
	if _, err := os.Stat(dst); err == nil {
		return nil // immutable object already present
	} else if !os.IsNotExist(err) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return WriteFileAtomic(dst, in)
}

func (r *Registry) writeManifest(a *Asset) error {
	p := filepath.Join(r.root, "manifests", a.ID+".json")
	if _, err := os.Stat(p); err == nil {
		return nil // same digest means same immutable manifest identity
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(p, bytes.NewReader(b))
}

func (r *Registry) historyPath(logicalPath string) string {
	key := SHA256Bytes([]byte(filepath.Clean(logicalPath))).Value
	return filepath.Join(r.root, "paths", key+".json")
}

func (r *Registry) appendHistory(logicalPath, id string) error {
	p := r.historyPath(logicalPath)
	h := PathHistory{Path: logicalPath}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &h); err != nil {
			return NewError(CatInvalidEncoding, "", -1, -1, "decode path history: %v", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, old := range h.AssetIDs {
		if old == id {
			h.Current = id
			return r.writeHistory(&h)
		}
	}
	h.AssetIDs = append(h.AssetIDs, id)
	h.Current = id
	return r.writeHistory(&h)
}

func (r *Registry) writeHistory(h *PathHistory) error {
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(r.historyPath(h.Path), bytes.NewReader(b))
}

// History returns all retained revisions for a logical path.
func (r *Registry) History(logicalPath string) (*PathHistory, error) {
	var h PathHistory
	b, err := os.ReadFile(r.historyPath(logicalPath))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, NewError(CatInvalidEncoding, "", -1, -1, "decode path history: %v", err)
	}
	return &h, nil
}

// LoadAsset reads an immutable manifest by digest.
func (r *Registry) LoadAsset(id string) (*Asset, error) {
	b, err := os.ReadFile(filepath.Join(r.root, "manifests", id+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NewError(CatMissingAsset, "", -1, -1, "asset %s not found", id)
		}
		return nil, err
	}
	var a Asset
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, NewError(CatInvalidEncoding, "", -1, -1, "decode asset: %v", err)
	}
	return &a, nil
}
