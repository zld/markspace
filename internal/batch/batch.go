// Package batch drives the pipeline end to end: manifest parse, segment scan,
// fingerprint dedup, chunked publish and index commit. It is the only package
// that writes artifacts and index state, keeping the crash-ordering invariant
// in one place: artifact bytes are written first, the index commit second.
package batch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediabatch/internal/checkpoint"
	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
	"mediabatch/internal/manifest"
	"mediabatch/internal/recovery"
	"mediabatch/internal/report"
	"mediabatch/internal/scan"
)

// Options configure one run.
type Options struct {
	WorkDir         string
	InputDir        string
	ChunkSize       int
	MaxSegmentBytes int64
	// CrashPoint is "", "after-artifact" or "after-commit".
	CrashPoint string
}

// Unit is one segment to process.
type Unit struct {
	Manifest       string
	Representation string
	Ref            string
	Path           string
	InitPath       string
	Start          clock.Time
	End            clock.Time
	Discontinuity  bool
	Expect         *scan.Expectation
}

// Identity returns the checkpoint identity of the unit. contentHash is the
// digest computed by the scan, so an in-place content change invalidates the
// chunk even when its timing and path are unchanged.
func (u Unit) Identity(contentHash string) checkpoint.Identity {
	return checkpoint.Identity{
		Manifest: u.Manifest, Representation: u.Representation, Segment: u.Ref,
		Start: u.Start, End: u.End, Path: u.Path, ContentHash: contentHash,
	}
}

// Outcome is the result of processing one unit.
type Outcome struct {
	Unit      Unit
	Result    scan.Result
	Key       string
	Artifact  string
	Duplicate bool
	Published bool
	Reused    bool
	Chunk     int
}

// Run is the result of a batch run.
// Crash points understood by --inject-crash.
const (
	CrashAfterArtifact = "after-artifact"
	CrashAfterCommit   = "after-commit"
)

const (
	crashAfterArtifact = CrashAfterArtifact
	crashAfterCommit   = CrashAfterCommit
)

// Run is the result of a batch run.
type Run struct {
	Issues           fault.List
	RecoveryIssues   fault.List
	Plan             recovery.Plan
	Outcomes         []Outcome
	Chunks           []checkpoint.Chunk
	Counts           report.Counts
	Report           *report.Report
	CrashPoint       string
	Crashed          bool
	AdoptedArtifacts int
	NewArtifacts     int
}

// CollectUnits parses every manifest under inputDir and returns the ordered
// unit list. Ordering is (start, manifest, representation, ref) so chunk
// boundaries are stable between runs.
func CollectUnits(inputDir string, maxBytes int64) ([]Unit, fault.List, error) {
	bundle, err := manifest.LoadDir(inputDir)
	if err != nil {
		return nil, nil, err
	}
	expect, expErr := loadExpectations(filepath.Join(inputDir, "expectations.json"))
	var issues fault.List
	issues = append(issues, bundle.Issues...)
	if expErr != nil && !os.IsNotExist(expErr) {
		issues = append(issues, fault.Issue{
			Code: fault.CodeParse, Severity: fault.SeverityWarning, Action: fault.ActionRecorded,
			Message: fmt.Sprintf("expectations unavailable: %v", expErr),
			Source:  fault.Source{Manifest: "expectations", Loc: fault.Locator{File: "expectations.json"}},
		})
	}
	var units []Unit
	for _, m := range bundle.Manifests {
		rel, _ := filepath.Rel(inputDir, m.File)
		initPath := filepath.Join(filepath.Dir(m.File), "init.mp4")
		for _, s := range m.Segments {
			if s.Isolated {
				continue
			}
			path := manifest.Resolve(m.File, s.URL)
			urel, err := filepath.Rel(inputDir, path)
			if err != nil || strings.HasPrefix(urel, "..") {
				continue
			}
			u := Unit{
				Manifest: filepath.ToSlash(rel), Representation: s.Representation,
				Ref: s.URL, Path: path, InitPath: initPath,
				Start: s.Start, End: s.End, Discontinuity: s.Discontinuity,
			}
			if e, ok := expect[filepath.ToSlash(urel)]; ok {
				u.Expect = &scan.Expectation{SHA256: e.SHA256, Size: e.Size}
			}
			units = append(units, u)
		}
	}
	sort.SliceStable(units, func(i, j int) bool {
		a, b := units[i], units[j]
		if c := a.Start.Cmp(b.Start); c != 0 {
			return c < 0
		}
		if a.Manifest != b.Manifest {
			return a.Manifest < b.Manifest
		}
		if a.Representation != b.Representation {
			return a.Representation < b.Representation
		}
		return a.Ref < b.Ref
	})
	issues.Sort()
	return units, issues, nil
}

type expected struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func loadExpectations(path string) (map[string]expected, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]expected
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
