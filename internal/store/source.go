// Package store persists the replay kernel's state. Source assets are written
// once and sealed read-only; derived data is content addressed so a rerun with
// identical inputs hits the cache instead of recomputing.
package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"replaycheck/internal/model"
)

// ErrSealed indicates an attempt to rewrite an immutable source asset.
var ErrSealed = errors.New("source asset is sealed")

// Source is an immutable input asset stored under a root directory.
type Source struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// SourceStore manages the sealed input directory.
type SourceStore struct {
	Root string
}

func NewSourceStore(root string) *SourceStore { return &SourceStore{Root: root} }

// safeJoin rejects names that could escape the store root.
func safeJoin(root, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty asset name")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("asset name contains NUL")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("asset name %q must be relative", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("asset name %q escapes store root", name)
	}
	joined := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("asset path escapes store root")
	}
	return joined, nil
}

// Put copies src into the source store under name. If the asset already exists
// with the same digest it is reused; if it exists with a different digest the
// write is refused, because source assets never change identity in place.
func (s *SourceStore) Put(name, src string) (Source, error) {
	dst, err := safeJoin(s.Root, name)
	if err != nil {
		return Source{}, model.WrapError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, "unsafe asset name", err)
	}
	digest, size, err := hashFile(src)
	if err != nil {
		return Source{}, model.WrapError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, "cannot hash source", err)
	}
	if _, err := os.Stat(dst); err == nil {
		oldDigest, _, herr := hashFile(dst)
		if herr != nil {
			return Source{}, model.WrapError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, "cannot hash existing asset", herr)
		}
		if oldDigest != digest {
			return Source{}, model.NewError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, ErrSealed.Error())
		}
		return Source{Name: name, Path: dst, Digest: digest, Size: size}, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Source{}, model.WrapError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, "cannot create asset dir", err)
	}
	if err := copyFile(src, dst); err != nil {
		return Source{}, model.WrapError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, "cannot copy asset", err)
	}
	if err := os.Chmod(dst, 0o444); err != nil {
		return Source{}, model.WrapError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, "cannot seal asset", err)
	}
	again, againSize, err := hashFile(dst)
	if err != nil || again != digest || againSize != size {
		return Source{}, model.NewError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, "asset digest changed after copy")
	}
	return Source{Name: name, Path: dst, Digest: digest, Size: size}, nil
}

// Open returns the sealed asset. The handle is read-only by construction.
func (s *SourceStore) Open(name string) (*os.File, Source, error) {
	path, err := safeJoin(s.Root, name)
	if err != nil {
		return nil, Source{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, Source{}, model.WrapError(model.StageIngest, "store", model.TrackRef{Name: name}, 0, "cannot open asset", err)
	}
	digest, size, err := hashFile(path)
	if err != nil {
		f.Close()
		return nil, Source{}, err
	}
	return f, Source{Name: name, Path: path, Digest: digest, Size: size}, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h, n, err := model.HashReader(f)
	if err != nil {
		return "", 0, err
	}
	return h, n, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
