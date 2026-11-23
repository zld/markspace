package media

import (
	"io"
	"os"
	"path/filepath"
)

// Remux rewrites the mapped tracks into a fresh container. Compressed sample
// bytes are copied verbatim; durations, decode order, composition offsets and
// sync flags are preserved. The write is atomic: a temp file in the output
// directory is renamed into place only after verification succeeds.
func Remux(src io.ReadSeeker, p *Probe, m *TrackMap, outPath string, opts RemuxOptions) (*RemuxResult, error) {
	if p == nil || p.Movie == nil {
		return nil, NewError(ClassInvalidArgument, "remux", 0, "missing probe")
	}
	if m == nil || len(m.Entries) == 0 {
		return nil, NewError(ClassInvalidArgument, "remux", 0, "empty track map")
	}
	absOut, err := ValidateOutPath(opts.OutDir, outPath)
	if err != nil {
		return nil, err
	}
	tracks, err := m.MappedTracks(p)
	if err != nil {
		return nil, err
	}
	movieTS := opts.MovieTimescale
	if movieTS == 0 {
		movieTS = p.Timescale
	}
	if movieTS == 0 {
		return nil, NewError(ClassTimebase, "remux", 0, "movie timescale is zero")
	}
	brand := opts.Brand
	if brand == "" {
		brand = p.Brand
		if brand == "" {
			brand = "isom"
		}
	}

	// Validate all input samples before writing anything.
	if err := ValidateMovieBounds(p); err != nil {
		return nil, err
	}
	for _, ti := range tracks {
		if err := CheckMonotonic(ti.Table); err != nil {
			return nil, err
		}
	}

	srcHashes := make(map[uint32]string, len(tracks))
	for _, ti := range tracks {
		h, err := hashSampleStream(src, ti.Table)
		if err != nil {
			return nil, err
		}
		srcHashes[ti.TrackID] = h
	}

	if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
		return nil, WrapError(ClassIO, "remux.mkdir", 0, err, "prepare output directory")
	}
	tmp, err := os.CreateTemp(filepath.Dir(absOut), ".remux-*.tmp")
	if err != nil {
		return nil, WrapError(ClassIO, "remux.tmp", 0, err, "create temp file")
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	res, err := writeRemux(tmp, src, tracks, movieTS, brand)
	if err != nil {
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, WrapError(ClassIO, "remux.sync", 0, err, "fsync temp file")
	}
	if err := tmp.Close(); err != nil {
		return nil, WrapError(ClassIO, "remux.close", 0, err, "close temp file")
	}

	// Verify by re-probing the temp file before it becomes visible.
	if err := verifyRemux(tmpName, src, tracks, res); err != nil {
		os.Remove(tmpName)
		return nil, err
	}
	if err := os.Rename(tmpName, absOut); err != nil {
		os.Remove(tmpName)
		return nil, WrapError(ClassIO, "remux.rename", 0, err, "atomic rename failed")
	}
	committed = true
	res.OutputPath = absOut
	if fi, err := os.Stat(absOut); err == nil {
		res.OutputSize = fi.Size()
	}
	res.SourceHash = srcHashes
	return res, nil
}
