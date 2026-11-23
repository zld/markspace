package index

import (
	"fmt"
	"os"
	"path/filepath"

	"avbatch/internal/mediaerr"
)

// ResumeWriter reopens the staging file left behind by a crash, keeps every
// verified block and positions the writer at the end of the recovered prefix.
// Unverified bytes are discarded, so the rewritten index can only contain
// blocks that passed their checksums.
func ResumeWriter(cfg Config, r *Reader) (*Writer, error) {
	cfg, err := cfg.validated()
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("index: nil reader")
	}
	if r.Recovery.DigestMismatch {
		return nil, mediaerr.New(mediaerr.KindResumeMismatch, -1, "index", 0,
			"input digest changed; refusing to resume")
	}
	staging := cfg.Path + ".tmp"
	if _, statErr := os.Stat(staging); statErr != nil {
		// No staging file: the crash happened before the first block. Start
		// over from a clean header.
		return NewWriter(cfg)
	}
	f, err := os.OpenFile(staging, os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	// Drop the damaged tail before appending anything.
	if err := f.Truncate(headerSize + r.Recovery.ResumeOffset); err != nil {
		f.Close()
		return nil, err
	}
	w := &Writer{
		cfg:  cfg,
		file: f,
		hdr: Header{
			Version:     FormatVersion,
			InputDigest: cfg.InputDigest,
			BlockSize:   cfg.BlockSamples,
			Blocks:      uint64(r.Recovery.BlocksValid),
			EntryCount:  len(r.Entries),
			OffsetBytes: r.Recovery.ResumeOffset,
		},
	}
	if err := w.writeHeader(); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// VerifyRepeat writes the same entry list twice to separate paths and reports
// whether the manifest digests agree. It is the offline determinism check
// behind --verify-repeat.
func VerifyRepeat(cfg Config, entries []Entry) (match bool, first, second [32]byte, err error) {
	dir, err := os.MkdirTemp("", "avbatch-repeat-")
	if err != nil {
		return false, first, second, err
	}
	defer os.RemoveAll(dir)

	for i := 0; i < 2; i++ {
		cfg.Path = filepath.Join(dir, fmt.Sprintf("run%d.idx", i))
		w, err := NewWriter(cfg)
		if err != nil {
			return false, first, second, err
		}
		for _, e := range entries {
			if _, err := w.Add(e); err != nil {
				w.Close()
				return false, first, second, err
			}
		}
		if _, err := w.Commit(); err != nil {
			return false, first, second, err
		}
		read, err := Open(cfg.Path, cfg.InputDigest)
		if err != nil {
			return false, first, second, err
		}
		d, err := ManifestDigest(read.Entries)
		if err != nil {
			return false, first, second, err
		}
		if i == 0 {
			first = d
		} else {
			second = d
		}
	}
	return first == second, first, second, nil
}
