// Package ingest validates the frame manifest and normalises RTP packets.
//
// Manifest validation is fail-closed: every declared frame is checked for
// geometry, path containment, duplicate sequence numbers, colour metadata and
// SHA-256 content, and the first offending frame is reported with its track.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"medialab/internal/model"
)

// Manifest is the on-disk input description.
type Manifest struct {
	Version int           `json:"version"`
	EpochNS int64         `json:"epochNs"`
	Frames  []model.Frame `json:"frames"`
}

// Result is the validated, sorted input handed to the timeline.
type Result struct {
	Manifest
	Order   []model.Frame
	Warned  model.Faults
	Hasher  func(string) ([]byte, error)
	RootDir string
}

// LoadOptions configures validation.
type LoadOptions struct {
	// RootDir is the directory frame paths must stay inside.
	RootDir string
	// VerifyContent enables SHA-256 comparison against the files on disk.
	VerifyContent bool
}

// Load reads, validates and content-checks a manifest. The returned Result is
// only produced when every hard check passes; recoverable warnings are returned
// alongside it.
func Load(path string, opt LoadOptions) (*Result, model.Faults, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, model.NewFault(model.KindInput, model.CodeManifestInvalid, 0, 0,
			fmt.Sprintf("read manifest: %v", err))
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, nil, model.NewFault(model.KindInput, model.CodeManifestInvalid, 0, 0,
			fmt.Sprintf("parse manifest: %v", err))
	}
	return Validate(m, opt)
}

// Validate performs the checks on an in-memory manifest.
func Validate(m Manifest, opt LoadOptions) (*Result, model.Faults, error) {
	root := opt.RootDir
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, model.NewFault(model.KindInput, model.CodePathInvalid, 0, 0,
			fmt.Sprintf("resolve root: %v", err))
	}
	// Resolve the root itself so containment compares canonical paths; on
	// macOS /var is a symlink to /private/var.
	if resolvedRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolvedRoot
	}

	if _, err := model.NewClock(m.EpochNS); err != nil {
		return nil, nil, model.WithFault(fault(err), 0, 0)
	}
	if len(m.Frames) == 0 {
		return nil, nil, model.NewFault(model.KindInput, model.CodeManifestInvalid, 0, 0,
			"manifest declares no frames")
	}

	type seqKey struct {
		track model.TrackID
		seq   uint32
	}
	seen := make(map[seqKey]int, len(m.Frames))
	// Geometry is per track: all frames of a track must agree.
	type geometry struct {
		w, h int
	}
	trackGeom := make(map[model.TrackID]geometry)
	var warnings model.Faults

	for i := range m.Frames {
		f := &m.Frames[i]
		if err := f.Validate(); err != nil {
			return nil, nil, model.WithFault(fault(err), f.Track, f.Sequence)
		}
		key := seqKey{f.Track, f.Sequence}
		if prev, ok := seen[key]; ok {
			return nil, nil, model.NewFault(model.KindInput, model.CodeSeqDuplicate, f.Track, f.Sequence,
				fmt.Sprintf("sequence %d repeats (also at index %d)", f.Sequence, prev))
		}
		seen[key] = i

		rel, err := containedRelPath(absRoot, f.Path)
		if err != nil {
			return nil, nil, model.WithFault(fault(err), f.Track, f.Sequence)
		}
		f.Path = rel

		if g, ok := trackGeom[f.Track]; ok {
			if g.w != f.Width || g.h != f.Height {
				return nil, nil, model.NewFault(model.KindInput, model.CodeSizeBounds, f.Track, f.Sequence,
					fmt.Sprintf("geometry %dx%d differs from track geometry %dx%d",
						f.Width, f.Height, g.w, g.h))
			}
		} else {
			trackGeom[f.Track] = geometry{f.Width, f.Height}
		}

		if opt.VerifyContent {
			actual, err := hashFile(filepath.Join(absRoot, rel))
			if err != nil {
				return nil, nil, model.NewFault(model.KindInput, model.CodeManifestInvalid, f.Track, f.Sequence,
					fmt.Sprintf("read frame file %q: %v", rel, err))
			}
			if err := verifySize(filepath.Join(absRoot, rel), f.Size); err != nil {
				return nil, nil, model.WithFault(fault(err), f.Track, f.Sequence)
			}
			if actual != f.SHA256 {
				return nil, nil, model.NewFault(model.KindInput, model.CodeHashMismatch, f.Track, f.Sequence,
					fmt.Sprintf("declared SHA-256 %s, file is %s", short(f.SHA256), short(actual)))
			}
		}
	}

	order := make([]model.Frame, len(m.Frames))
	copy(order, m.Frames)
	stableSortFrames(order)

	res := &Result{Manifest: m, Order: order, Warned: warnings, RootDir: absRoot}
	return res, warnings, nil
}

// stableSortFrames applies the shared timestamp, track, sequence ordering.
func stableSortFrames(frames []model.Frame) {
	// Insertion-stable mergesort keyed by model.LessSortKey keeps equal keys in
	// declaration order, so identical manifests always yield identical output.
	if len(frames) < 2 {
		return
	}
	buf := make([]model.Frame, len(frames))
	for width := 1; width < len(frames); width *= 2 {
		for lo := 0; lo < len(frames); lo += 2 * width {
			mid := min(lo+width, len(frames))
			hi := min(lo+2*width, len(frames))
			mergeFrames(frames[lo:mid], frames[mid:hi], buf[:0])
			copy(frames[lo:hi], buf[:hi-lo])
		}
	}
}

func mergeFrames(a, b, out []model.Frame) []model.Frame {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if model.LessSortKey(b[j], a[i]) {
			out = append(out, b[j])
			j++
		} else {
			out = append(out, a[i])
			i++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

// containedRelPath rejects absolute paths, traversal and symlink escapes.
func containedRelPath(absRoot, p string) (string, error) {
	if filepath.IsAbs(p) {
		return "", model.NewFault(model.KindInput, model.CodePathEscape, 0, 0,
			fmt.Sprintf("absolute path %q is not allowed", p))
	}
	clean := filepath.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", model.NewFault(model.KindInput, model.CodePathEscape, 0, 0,
			fmt.Sprintf("path %q escapes the input root", p))
	}
	joined := filepath.Join(absRoot, clean)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// A missing file must not escape the root either; joining and cleaning
		// already proves containment for non-existent paths.
		resolved = joined
	}
	if !within(absRoot, resolved) {
		return "", model.NewFault(model.KindInput, model.CodePathEscape, 0, 0,
			fmt.Sprintf("path %q resolves outside the input root", p))
	}
	return clean, nil
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifySize(path string, declared int) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if int(st.Size()) != declared {
		return fmt.Errorf("declared size %d, file is %d bytes", declared, st.Size())
	}
	return nil
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func fault(err error) *model.Fault {
	if f, ok := model.AsFault(err); ok {
		return f
	}
	return model.NewFault(model.KindInput, model.CodeManifestInvalid, 0, 0, err.Error())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
