package verify

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"mediacore/internal/transform"
)

// Options names the artifacts to cross-check.
type Options struct {
	ManifestPath string
	FramesPath   string
}

// Report is the independent verification outcome for written artifacts.
type Report struct {
	ManifestSHA256 string            `json:"manifest_sha256"`
	FrameCount     int               `json:"frame_count"`
	FrameDigest    string            `json:"frame_digest"`
	Failures       []Failure         `json:"failures"`
	Manifest       *Manifest         `json:"-"`
	Frames         []transform.Frame `json:"-"`
}

// Files re-reads a manifest and a JSONL frame export and checks them against
// each other without trusting any in-memory state from the producing run.
func Files(opts Options) (*Report, error) {
	m, err := ReadManifest(opts.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	rep := &Report{ManifestSHA256: m.SHA256, Manifest: m}
	if opts.FramesPath == "" {
		if len(m.Frames) > 0 {
			rep.Failures = append(rep.Failures, Failure{Invariant: InvFrameSources, Detail: "manifest has frames but no frame export was given"})
		}
		return rep, nil
	}
	frames, err := readFrames(opts.FramesPath)
	if err != nil {
		return nil, fmt.Errorf("frames: %w", err)
	}
	rep.Frames = frames
	rep.FrameCount = len(frames)
	rep.FrameDigest = transform.DigestFrames(frames)
	rep.Failures = append(rep.Failures, verifyExport(m, frames)...)
	return rep, nil
}

func readFrames(path string) ([]transform.Frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var frames []transform.Frame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var fr transform.Frame
		if err := json.Unmarshal(raw, &fr); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		frames = append(frames, fr)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return frames, nil
}

// verifyExport checks the exported frame sequence against the manifest:
// contiguous numbering, non-decreasing time, per-frame provenance match and a
// root digest that agrees with the manifest.
func verifyExport(m *Manifest, frames []transform.Frame) []Failure {
	var out []Failure
	if len(m.Frames) != len(frames) {
		out = append(out, Failure{Invariant: InvManifestMatch,
			Detail: fmt.Sprintf("manifest records %d frames, export has %d", len(m.Frames), len(frames))})
	}
	n := len(frames)
	if len(m.Frames) < n {
		n = len(m.Frames)
	}
	for i := 0; i < n; i++ {
		got, want := frames[i], m.Frames[i]
		if got.Number != want.Number {
			out = append(out, Failure{Invariant: InvUniqueFrames, Frame: i,
				Detail: fmt.Sprintf("export frame %d numbered %d, manifest %d", i, got.Number, want.Number)})
		}
		if got.PixelSHA != want.PixelSHA {
			out = append(out, Failure{Invariant: InvManifestMatch, Frame: i, Detail: "pixel digest differs from manifest"})
		}
		if got.SourceOffset != want.SourceOffset || got.SourceSize != want.SourceSize || got.SourceFile != want.SourceFile {
			out = append(out, Failure{Invariant: InvManifestMatch, Frame: i, Detail: "source provenance differs from manifest"})
		}
		if got.SourceSHA != want.SourceSHA {
			out = append(out, Failure{Invariant: InvSourceChecksum, Frame: i, Detail: "source checksum differs from manifest"})
		}
		if i > 0 && got.TimelineTime < frames[i-1].TimelineTime {
			out = append(out, Failure{Invariant: InvFrameOrder, Frame: i, Detail: "exported frame time regressed"})
		}
		if got.Number != i {
			out = append(out, Failure{Invariant: InvUniqueFrames, Frame: i, Detail: "frame numbering is not contiguous"})
		}
	}
	// Recompute the root digest from the export using the same rule the
	// producer used and compare it with the manifest record.
	exportDigest := transform.DigestFrames(frames)
	if m.FrameDigest != "" && exportDigest != m.FrameDigest {
		out = append(out, Failure{Invariant: InvManifestMatch,
			Detail: fmt.Sprintf("export digest %s != manifest %s", short(exportDigest), short(m.FrameDigest))})
	}
	return out
}
