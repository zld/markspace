package recover

import (
	"errors"
	"io"
	"os"
	"strings"

	"mediapack/internal/commit"
	"mediapack/internal/fanout"
	"mediapack/internal/manifest"
	"mediapack/internal/safepath"
)

// resetManagedPaths removes only the fixed files/directories owned by this
// tool inside work. It never touches arbitrary user files.
func resetManagedPaths(work string) {
	segDir, err := safepath.JoinUnder(work, "segments")
	if err == nil {
		_ = os.RemoveAll(segDir)
	}
	for _, rel := range []string{
		manifest.ManifestName, manifest.ManifestName + ".tmp",
		commit.JournalName, "source.ts",
	} {
		if p, err := manifest.Resolve(work, rel); err == nil {
			_ = os.Remove(p)
		}
	}
}

func checkWork(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", errors.New("--work is required")
	}
	return safepath.ValidateLocalOutput(workDir)
}

func hashOK(work string, art *manifest.Artifact) bool {
	if art.Status != manifest.StatusCommitted {
		return false
	}
	p, err := manifest.Resolve(work, art.RelPath)
	if err != nil {
		return false
	}
	sum, n, err := manifest.HashFile(p)
	if err != nil {
		return false
	}
	return sum == art.SHA256 && n == art.Length
}

func countInvalid(work string, m *manifest.Manifest) int {
	bad := 0
	for i := range m.Artifacts {
		if !hashOK(work, &m.Artifacts[i]) {
			bad++
		}
	}
	return bad
}

// FragmentBytes rebuilds the deterministic fragment bytes for an artifact.
func FragmentBytes(src io.ReadSeeker, art *manifest.Artifact) ([]byte, error) {
	intervalID := art.ID
	if i := strings.LastIndex(art.ID, "/"); i >= 0 {
		intervalID = art.ID[i+1:]
	}
	h := fanout.Header{
		ID: intervalID, IntervalIndex: art.IntervalIndex, RateIndex: art.RateIndex,
		RateName: art.RateName, SourceStart: art.SourceStart, SourceEnd: art.SourceEnd,
		StartTick: art.StartTick, EndTick: art.EndTick,
		StructuralDigest: art.StructuralDigest,
	}
	return fanout.BuildFragment(fanout.BuildInput{
		Header: h, Keyframes: []int64{art.KeyframeOffset}, Source: src,
		SourceStart: art.SourceStart, SourceEnd: art.SourceEnd, EnableRTP: true,
	})
}

// openSource resolves and opens the ledger source file.
func openSource(work string, m *manifest.Manifest) (*os.File, error) {
	srcFull, err := manifest.Resolve(work, m.SourceRelPath)
	if err != nil {
		return nil, err
	}
	return os.Open(srcFull)
}
