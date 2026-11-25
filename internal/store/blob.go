package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"replaycheck/internal/model"
)

// Blob is an immutable, content-addressed byte artifact. The file name is its
// SHA-256, so identical derived clips from different runs occupy one file and
// a byte change necessarily produces a different address.
type BlobStore struct {
	Root string
}

func NewBlobStore(root string) *BlobStore { return &BlobStore{Root: root} }

// Path returns the file path for a digest, rejecting malformed addresses so a
// crafted digest cannot escape the store.
func (b *BlobStore) Path(digest string) (string, error) {
	if !model.ValidDigest(digest) {
		return "", model.NewError(model.StageDerive, "blob", model.TrackRef{}, 0, "invalid blob digest")
	}
	return filepath.Join(b.Root, digest[:2], digest), nil
}

// Put streams r into a new blob and returns its digest and size. The caller's
// buffer bounds memory use; the payload is never fully materialized.
func (b *BlobStore) Put(r io.Reader) (string, int64, error) {
	if err := os.MkdirAll(b.Root, 0o755); err != nil {
		return "", 0, model.WrapError(model.StageDerive, "blob", model.TrackRef{}, 0, "cannot create blob dir", err)
	}
	tmp, err := os.CreateTemp(b.Root, "blob-*.part")
	if err != nil {
		return "", 0, model.WrapError(model.StageDerive, "blob", model.TrackRef{}, 0, "cannot create temp blob", err)
	}
	tmpName := tmp.Name()
	digest, size, err := model.HashReaderStream(tmp, r)
	if cerr := tmp.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpName)
		return "", 0, model.WrapError(model.StageDerive, "blob", model.TrackRef{}, 0, "cannot write blob", err)
	}
	dst, err := b.Path(digest)
	if err != nil {
		os.Remove(tmpName)
		return "", 0, err
	}
	if _, err := os.Stat(dst); err == nil {
		os.Remove(tmpName) // deduplicated: same bytes already stored
		return digest, size, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		os.Remove(tmpName)
		return "", 0, model.WrapError(model.StageDerive, "blob", model.TrackRef{}, 0, "cannot create blob shard", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return "", 0, model.WrapError(model.StageDerive, "blob", model.TrackRef{}, 0, "cannot publish blob", err)
	}
	return digest, size, nil
}

// Open returns a read-only handle to a stored blob.
func (b *BlobStore) Open(digest string) (*os.File, error) {
	p, err := b.Path(digest)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, model.Errorf(model.StageDerive, "blob", model.TrackRef{}, 0, "blob %s is missing", digest[:12])
		}
		return nil, model.WrapError(model.StageDerive, "blob", model.TrackRef{}, 0, "cannot open blob", err)
	}
	return f, nil
}

// Size returns the stored size of a blob.
func (b *BlobStore) Size(digest string) (int64, error) {
	p, err := b.Path(digest)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return 0, model.WrapError(model.StageDerive, "blob", model.TrackRef{}, 0, "cannot stat blob", err)
	}
	return st.Size(), nil
}

// Recompute hashes a stored blob and returns its digest and size, which is how
// manifest verification proves a derived digest is reproducible.
func (b *BlobStore) Recompute(digest string) (string, int64, error) {
	f, err := b.Open(digest)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	sum, size, err := model.HashReader(f)
	if err != nil {
		return "", 0, model.WrapError(model.StageDerive, "blob", model.TrackRef{}, 0, "cannot hash blob", err)
	}
	return sum, size, nil
}
