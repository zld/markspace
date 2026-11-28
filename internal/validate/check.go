// Package validate checks a finished run before anything is published.
//
// Verification is itemized so every conclusion can be checked line by line:
// the MP4 sample count, monotonic decode timestamps, manifest and artifact
// hashes, the memory ceiling and the processing budget. Each item yields a
// PASS/FAIL line; a single FAIL makes the run fail without publishing.
package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"

	gomp4 "github.com/abema/go-mp4"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/fault"
	"rtp-rebuild/internal/manifest"
)

// Budget is the fixed resource envelope for one run.
type Budget struct {
	// MaxBufferBytes caps buffered data (4 MiB).
	MaxBufferBytes int64
	// MaxPackets caps buffered packets (256).
	MaxPackets int64
	// BudgetMS is the processing time budget.
	BudgetMS int64
	// ElapsedMS is the measured elapsed time.
	ElapsedMS int64
}

// DefaultBufferBytes is the 4 MiB input/buffer ceiling.
const DefaultBufferBytes = 4 << 20

// DefaultMaxPackets is the 256 packet pending ceiling.
const DefaultMaxPackets = 256

// Result is one check outcome.
type Result struct {
	Name   string
	Pass   bool
	Detail string
}

// Report is the full validation outcome.
type Report struct {
	Results []Result
}

// Passed reports whether every item passed.
func (r *Report) Passed() bool {
	for _, res := range r.Results {
		if !res.Pass {
			return false
		}
	}
	return true
}

// Add appends a check result.
func (r *Report) Add(name string, pass bool, detail string) {
	r.Results = append(r.Results, Result{Name: name, Pass: pass, Detail: detail})
}

// Failures returns the failed items.
func (r *Report) Failures() []Result {
	var out []Result
	for _, res := range r.Results {
		if !res.Pass {
			out = append(out, res)
		}
	}
	return out
}

// Counts returns passed/total.
func (r *Report) Counts() (passed, total int) {
	for _, res := range r.Results {
		total++
		if res.Pass {
			passed++
		}
	}
	return passed, total
}

// MP4Probe is the subset of the written track the validator inspects.
type MP4Probe struct {
	SampleCount uint32
	Timescale   uint32
	DTS         []uint64
	Duration    uint64
}

// ProbeMP4 reads the written track back from disk.
func ProbeMP4(path string) (*MP4Probe, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New(fault.StageValidate, "open mp4", err)
	}
	defer f.Close()

	probe := &MP4Probe{}
	var inStbl bool
	var pendingStts []gomp4.SttsEntry
	var mdhdTimescale uint32
	var mdhdDuration uint64

	_, err = gomp4.ReadBoxStructure(f, func(h *gomp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case gomp4.BoxTypeStbl():
			inStbl = true
		case gomp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mdhd := box.(*gomp4.Mdhd)
			mdhdTimescale = mdhd.Timescale
			mdhdDuration = mdhd.GetDuration()
		case gomp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			probe.SampleCount = box.(*gomp4.Stsz).SampleCount
		case gomp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			pendingStts = box.(*gomp4.Stts).Entries
		}
		return h.Expand()
	})
	if err != nil {
		return nil, fault.New(fault.StageValidate, "parse mp4", err)
	}
	if !inStbl {
		return nil, fault.New(fault.StageValidate, "parse mp4", fmt.Errorf("%w: no stbl box", fault.ErrCorrupt))
	}
	probe.Timescale = mdhdTimescale
	probe.Duration = mdhdDuration
	var dts uint64
	for _, e := range pendingStts {
		for i := uint32(0); i < e.SampleCount; i++ {
			probe.DTS = append(probe.DTS, dts)
			dts += uint64(e.SampleDelta)
		}
	}
	return probe, nil
}

// FileSHA256 hashes a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fault.New(fault.StageValidate, "open "+path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fault.New(fault.StageValidate, "hash "+path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Inputs bundles everything the validation items need.
type Inputs struct {
	// ArtifactPath is the published MP4 file.
	ArtifactPath string
	// ManifestPath is the manifest file, if it was published.
	ManifestPath string
	// Manifest is the in-memory manifest that will be written.
	Manifest *manifest.Manifest
	// ExpectedManifestSHA is the deterministic digest the manifest must have.
	ExpectedManifestSHA string
	// PreviousManifestSHA is the digest of a manifest published by an earlier
	// run with the same inputs; empty on a first run.
	PreviousManifestSHA string
	// ExpectedArtifactSHA is the digest of the artifact bytes.
	ExpectedArtifactSHA string
	// ExpectedSamples is the number of reconstructed packets.
	ExpectedSamples int
	// Gaps is the count of interval anomalies; any interval fails the run.
	Gaps int
	// LossRate is measured loss / total.
	LossRate float64
	// LossRateMax is the fail threshold (0.02).
	LossRateMax float64
	// PeakBufferBytes is the measured buffering high-water mark.
	PeakBufferBytes int64
	// PeakPackets is the measured pending-packet high-water mark.
	PeakPackets int64
	Budget      Budget
}

// Run performs every check item.
func Run(in Inputs) (*Report, error) {
	rep := &Report{}

	probe, err := ProbeMP4(in.ArtifactPath)
	if err != nil {
		rep.Add("mp4_parse", false, err.Error())
		return rep, err
	}
	rep.Add("mp4_sample_count", int(probe.SampleCount) == in.ExpectedSamples,
		fmt.Sprintf("samples=%d expected=%d", probe.SampleCount, in.ExpectedSamples))

	monotonic := true
	for i := 1; i < len(probe.DTS); i++ {
		if probe.DTS[i] < probe.DTS[i-1] {
			monotonic = false
			break
		}
	}
	rep.Add("mp4_dts_monotonic", monotonic && len(probe.DTS) == int(probe.SampleCount),
		fmt.Sprintf("samples=%d dts_entries=%d", probe.SampleCount, len(probe.DTS)))

	rep.Add("mp4_timescale", probe.Timescale == clock.TickRate,
		fmt.Sprintf("timescale=%d expected=%d", probe.Timescale, clock.TickRate))

	artifactSHA, err := FileSHA256(in.ArtifactPath)
	if err != nil {
		rep.Add("artifact_hash", false, err.Error())
		return rep, err
	}
	rep.Add("artifact_hash", artifactSHA == in.ExpectedArtifactSHA,
		fmt.Sprintf("%s", artifactSHA))

	if in.Manifest == nil {
		rep.Add("manifest_hash", false, "manifest missing")
	} else if computed, err := in.Manifest.SHA256(); err != nil {
		rep.Add("manifest_hash", false, err.Error())
	} else {
		rep.Add("manifest_hash", computed == in.ExpectedManifestSHA, computed)
		detail := "no previous manifest"
		pass := true
		if in.PreviousManifestSHA != "" {
			pass = in.PreviousManifestSHA == computed
			detail = fmt.Sprintf("previous=%s current=%s", in.PreviousManifestSHA, computed)
		}
		rep.Add("manifest_repeatable", pass, detail)
	}

	rep.Add("memory_ceiling",
		in.PeakBufferBytes <= in.Budget.MaxBufferBytes && in.PeakPackets <= in.Budget.MaxPackets,
		fmt.Sprintf("bytes=%d/%d packets=%d/%d", in.PeakBufferBytes, in.Budget.MaxBufferBytes,
			in.PeakPackets, in.Budget.MaxPackets))

	delta := in.Budget.ElapsedMS - in.Budget.BudgetMS
	rep.Add("time_budget", delta <= 0,
		fmt.Sprintf("elapsed_ms=%d budget_ms=%d delta_ms=%d", in.Budget.ElapsedMS, in.Budget.BudgetMS, delta))

	rep.Add("loss_rate", in.LossRate <= in.LossRateMax,
		fmt.Sprintf("loss_rate=%.4f max=%.4f", in.LossRate, in.LossRateMax))

	rep.Add("gap_intervals", in.Gaps == 0,
		fmt.Sprintf("gap_intervals=%d", in.Gaps))

	return rep, nil
}

// SortedArtifacts returns artifacts ordered by name, for deterministic manifests.
func SortedArtifacts(m *manifest.Manifest) []manifest.Artifact {
	out := append([]manifest.Artifact(nil), m.Artifacts...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
