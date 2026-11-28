// Package remux performs lossless container-to-container rewriting. Compressed
// sample bytes are copied verbatim from the parsed source; writes go through a
// temp file plus completion marker so an interrupted run never leaves a
// trusted-looking artifact, and reruns verify input/output summaries before
// reusing or rebuilding anything.
package remux

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
)

// Manifest is the completion marker written beside the output container.
type Manifest struct {
	// InputSHA256 summarizes the exact source bytes the output derives from.
	InputSHA256 string `json:"input_sha256"`
	// OutputSHA256 summarizes every byte of the finished container.
	OutputSHA256 string `json:"output_sha256"`
	// SampleHashes are per-track per-sample content hashes proving byte
	// preservation across the remux.
	SampleHashes [][]string `json:"sample_hashes"`
	// TrackCount and SampleCounts guard against dropped tracks/samples.
	TrackCount   int   `json:"track_count"`
	SampleCounts []int `json:"sample_counts"`
}

// Result is what a successful Write returns.
type Result struct {
	OutputPath string
	Manifest   Manifest
	// Rebuilt is false when a verified existing output was reused, true when
	// a fresh container had to be produced.
	Rebuilt bool
}

// Write losslessly remuxes the parsed container. r is the original seekable
// source used to copy sample payloads without buffering them all in memory.
// The output lands at outPath; a sibling ".done" marker holds the manifest.
// Repeated calls with identical input are idempotent and byte-stable.
func Write(r io.ReaderAt, c *demux.Container, inSHA []byte, outPath string) (*Result, error) {
	markerPath := outPath + ".done"

	// Fast path: a finished output whose manifest matches the current input.
	if res, ok, err := reuseIfValid(r, c, inSHA, outPath, markerPath); err != nil {
		return nil, err
	} else if ok {
		return res, nil
	}

	// Any stale or partial output is removed before rebuilding so corruption
	// can never be mistaken for a finished deliverable.
	for _, p := range []string{outPath, markerPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return nil, merr.New(merr.CodeCorruptArtifact, "cannot remove stale output").Wrap(err)
		}
	}

	dir := filepath.Dir(outPath)
	tmp, err := os.CreateTemp(dir, ".remux-*")
	if err != nil {
		return nil, merr.New(merr.CodeCorruptArtifact, "temp file create failed").Wrap(err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return nil, merr.New(merr.CodeCorruptArtifact, "temp chmod failed").Wrap(err)
	}
	tmpName := tmp.Name()
	// On any failure the temp file is unlinked; nothing is renamed.
	defer func() { _ = os.Remove(tmpName) }()

	manifest, err := serialize(r, c, inSHA, tmp)
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, merr.New(merr.CodeCorruptArtifact, "temp sync failed").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return nil, merr.New(merr.CodeCorruptArtifact, "temp close failed").Wrap(err)
	}

	// Verify the just-written bytes against the manifest before promotion.
	if err := verifyFileHash(tmpName, manifest.OutputSHA256); err != nil {
		return nil, err
	}
	if err := writeMarker(markerPath, manifest); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return nil, merr.New(merr.CodeCorruptArtifact, "atomic rename failed").Wrap(err)
	}
	return &Result{OutputPath: outPath, Manifest: manifest, Rebuilt: true}, nil
}

// serialize streams the source sample bytes into the builder and records all
// hashes while doing so.
func serialize(r io.ReaderAt, c *demux.Container, inSHA []byte, w io.Writer) (Manifest, error) {
	builder := newBuilder()
	manifest := Manifest{InputSHA256: hex.EncodeToString(inSHA), TrackCount: len(c.Tracks)}
	outHash := sha256.New()
	sink := io.MultiWriter(w, outHash)

	for ti, tr := range c.Tracks {
		hashes := make([]string, 0, len(tr.Samples))
		count := 0
		for si := range tr.Samples {
			payload, err := c.ReadSample(r, tr, si)
			if err != nil {
				return manifest, err
			}
			h := sha256.Sum256(payload)
			hashes = append(hashes, hex.EncodeToString(h[:]))
			builder.addSample(tr, payload)
			count++
		}
		manifest.SampleHashes = append(manifest.SampleHashes, hashes)
		manifest.SampleCounts = append(manifest.SampleCounts, count)
		_ = ti
	}
	container, err := builder.finish(c)
	if err != nil {
		return manifest, err
	}
	if _, err := sink.Write(container); err != nil {
		return manifest, merr.New(merr.CodeCorruptArtifact, "output write failed").Wrap(err)
	}
	manifest.OutputSHA256 = hex.EncodeToString(outHash.Sum(nil))
	return manifest, nil
}

func reuseIfValid(r io.ReaderAt, c *demux.Container, inSHA []byte, outPath, markerPath string) (*Result, bool, error) {
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, merr.New(merr.CodeCorruptArtifact, "marker read failed").Wrap(err)
	}
	var m Manifest
	if err := json.Unmarshal(marker, &m); err != nil {
		return nil, false, nil // corrupt marker -> rebuild
	}
	if m.InputSHA256 != hex.EncodeToString(inSHA) {
		return nil, false, nil // input changed -> rebuild
	}
	if err := verifyFileHash(outPath, m.OutputSHA256); err != nil {
		// Corrupt artifact: safe to rebuild from the intact input.
		return nil, false, nil
	}
	if err := verifySampleHashes(r, c, m); err != nil {
		return nil, false, nil
	}
	return &Result{OutputPath: outPath, Manifest: m, Rebuilt: false}, true, nil
}

func verifyFileHash(path, wantHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return merr.New(merr.CodeCorruptArtifact, "output open failed").Wrap(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return merr.New(merr.CodeCorruptArtifact, "output hash failed").Wrap(err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHex {
		return merr.New(merr.CodeCorruptArtifact,
			fmt.Sprintf("artifact hash %s does not match manifest %s", got, wantHex)).
			WithState(merr.StateFatal)
	}
	return nil
}

func verifySampleHashes(r io.ReaderAt, c *demux.Container, m Manifest) error {
	if len(m.SampleHashes) != len(c.Tracks) {
		return merr.New(merr.CodeCorruptArtifact, "manifest track count mismatch")
	}
	for ti, tr := range c.Tracks {
		if len(m.SampleHashes[ti]) != len(tr.Samples) {
			return merr.New(merr.CodeCorruptArtifact, "manifest sample count mismatch")
		}
		for si := range tr.Samples {
			payload, err := c.ReadSample(r, tr, si)
			if err != nil {
				return err
			}
			h := sha256.Sum256(payload)
			if hex.EncodeToString(h[:]) != m.SampleHashes[ti][si] {
				return merr.New(merr.CodeCorruptArtifact,
					fmt.Sprintf("sample t%d s%d changed since marker", ti, si))
			}
		}
	}
	return nil
}

func writeMarker(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return merr.New(merr.CodeCorruptArtifact, "marker marshal failed").Wrap(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return merr.New(merr.CodeCorruptArtifact, "marker write failed").Wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return merr.New(merr.CodeCorruptArtifact, "marker rename failed").Wrap(err)
	}
	return nil
}

// HashBytes returns the SHA-256 of a complete in-memory source.
func HashBytes(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
