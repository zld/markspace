package mediacore

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// InputDigest identifies the exact bytes of an input file. It covers both a
// cheap header digest of offset+size and a strong content hash, so a cache
// can reject a stale index before parsing anything.
type InputDigest struct {
	Size    int64  `json:"size"`
	Content string `json:"content"`
}

// String renders the digest in a stable, copyable form.
func (d InputDigest) String() string { return d.Content }

// DigestFile streams path and returns its content digest without loading the
// whole file into memory.
func DigestFile(path string) (InputDigest, error) {
	f, err := os.Open(path)
	if err != nil {
		return InputDigest{}, wrapErr(KindIO, "digest", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return InputDigest{}, wrapErr(KindIO, "digest", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return InputDigest{}, wrapErr(KindIO, "digest", err)
	}
	return InputDigest{Size: st.Size(), Content: hex.EncodeToString(h.Sum(nil))}, nil
}

// DigestReader hashes an arbitrary reader (used for in-memory fixtures).
func DigestReader(r io.Reader) (InputDigest, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return InputDigest{}, wrapErr(KindIO, "digest", err)
	}
	return InputDigest{Size: n, Content: hex.EncodeToString(h.Sum(nil))}, nil
}

// Verify reports whether the digest still matches path. A changed size fails
// immediately without reading the rest of the file.
func (d InputDigest) Verify(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return wrapErr(KindIO, "digest", err)
	}
	if st.Size() != d.Size {
		return wrapErr(KindDigest, "digest", ErrIndexStale)
	}
	fresh, err := DigestFile(path)
	if err != nil {
		return err
	}
	if fresh.Content != d.Content {
		return wrapErr(KindDigest, "digest", ErrIndexStale)
	}
	return nil
}
