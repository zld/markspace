package index

import (
	"io"

	"mediapack/internal/probe"
)

// copyBuffer is io.CopyBuffer without the nil-buffer panic path.
func copyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	return io.CopyBuffer(dst, src, buf)
}

// GetOrScan returns a cached entry when the input is unchanged, otherwise
// rescans with lim and persists the fresh entry. Any probe error carries the
// original byte-offset information unchanged.
func (s *Store) GetOrScan(path string, lim probe.Limits) (*Entry, error) {
	sum, err := Summarize(path)
	if err != nil {
		return nil, err
	}
	existing, err := s.Lookup(sum)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	meta, err := probe.Scan(path, lim)
	if err != nil {
		return nil, err
	}
	return s.Save(sum, meta)
}

// MustRescan discards any cached state and scans the input again.
func (s *Store) MustRescan(path string, lim probe.Limits) (*Entry, error) {
	sum, err := Summarize(path)
	if err != nil {
		return nil, err
	}
	if err := s.Invalidate(sum); err != nil {
		return nil, err
	}
	meta, err := probe.Scan(path, lim)
	if err != nil {
		return nil, err
	}
	return s.Save(sum, meta)
}
