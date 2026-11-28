// Package verify independently re-reads rendered artefacts and checks every
// constraint against the files themselves. It never trusts the renderer's
// in-memory state: each frame is re-hashed, re-decoded and re-measured, each
// segment is re-parsed, and any constraint breach is a hard verify fault that
// maps to exit status 2.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"medialab/internal/artifact"
	"medialab/internal/fixtures/mp4seg"
	"medialab/internal/model"
	"medialab/internal/pixel"
	"medialab/internal/render"
)

// Config is the verification envelope. Zero values disable the individual cap.
type Config struct {
	MaxDimension    int
	MaxAlignErrorNS int64
	MaxBytes        int64
	MinLoudnessDBFS float64
	MaxVariance     float64
	// MaxLoudnessDBFS rejects unexpectedly hot artefacts.
	MaxLoudnessDBFS float64
	// VarianceTolerance is the relative allowance when comparing the manifest
	// statistics against freshly decoded pixels.
	VarianceTolerance float64
}

// Report is the verifier's verdict.
type Report struct {
	ManifestPath string       `json:"manifestPath"`
	Frames       int          `json:"frames"`
	Fills        int          `json:"fills"`
	Segments     int          `json:"segments"`
	TotalBytes   int64        `json:"totalBytes"`
	MeanR        float64      `json:"meanR"`
	MeanG        float64      `json:"meanG"`
	MeanB        float64      `json:"meanB"`
	Variance     float64      `json:"variance"`
	PeakLoudness float64      `json:"peakLoudnessDbfs"`
	MaxAlignNS   int64        `json:"maxAlignErrorNs"`
	RunHash      string       `json:"runHash"`
	Faults       model.Faults `json:"faults,omitempty"`
}

// LoadManifest reads a manifest as untrusted data.
func LoadManifest(outDir string) (*artifact.Manifest, error) {
	path := filepath.Join(outDir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, model.NewFault(model.KindVerify, model.CodeArtifactMismatch, 0, 0,
			fmt.Sprintf("read manifest: %v", err))
	}
	var m artifact.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, model.NewFault(model.KindVerify, model.CodeArtifactMismatch, 0, 0,
			fmt.Sprintf("parse manifest: %v", err))
	}
	if m.Version != artifact.Version {
		return nil, model.NewFault(model.KindVerify, model.CodeArtifactMismatch, 0, 0,
			fmt.Sprintf("manifest version %d, want %d", m.Version, artifact.Version))
	}
	return &m, nil
}

// Verify re-reads and checks every artefact under outDir.
func Verify(outDir string, cfg Config) (*Report, error) {
	m, err := LoadManifest(outDir)
	if err != nil {
		return nil, err
	}
	rep := &Report{ManifestPath: filepath.Join(outDir, "manifest.json")}

	seenPath := make(map[string]bool, len(m.Frames))
	seenKey := make(map[string]bool, len(m.Frames))
	var verifiedFrames []artifact.Frame
	var sr, sg, sb, sv float64
	rep.PeakLoudness = pixel.LoudnessFloorDBFS
	var sizeSum int64

	for _, f := range m.Frames {
		rel := f.Path
		if err := safeJoin(outDir, rel); err != nil {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence, err.Error()))
			continue
		}
		if seenPath[rel] {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence,
				fmt.Sprintf("duplicate artefact path %q", rel)))
			continue
		}
		seenPath[rel] = true
		key := fmt.Sprintf("%d/%d", f.Track, f.Sequence)
		if seenKey[key] {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence,
				fmt.Sprintf("duplicate frame %s in manifest", key)))
			continue
		}
		seenKey[key] = true

		data, err := os.ReadFile(filepath.Join(outDir, rel))
		if err != nil {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence,
				fmt.Sprintf("missing artefact: %v", err)))
			continue
		}
		if got := hashHex(data); got != f.SHA256 {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence,
				fmt.Sprintf("artefact hash %s does not match recorded %s", short(got), short(f.SHA256))))
			continue
		}
		if int64(len(data)) != f.Bytes {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence,
				fmt.Sprintf("artefact size %d does not match recorded %d", len(data), f.Bytes)))
		}
		raw, err := render.DecodePNG(data)
		if err != nil {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence,
				fmt.Sprintf("decode artefact: %v", err)))
			continue
		}
		if err := checkGeometry(raw, f, cfg); err != nil {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence, err.Error()))
		}
		if err := checkStats(raw, f, cfg); err != nil {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence, err.Error()))
		}
		stats, err := pixel.Measure(raw)
		if err != nil {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence, err.Error()))
			continue
		}
		loud, err := pixel.LoudnessPeakDBFS(raw)
		if err != nil {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence, err.Error()))
			continue
		}
		if cfg.MaxLoudnessDBFS != 0 && loud > cfg.MaxLoudnessDBFS {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence,
				fmt.Sprintf("loudness %.2f dBFS exceeds %.2f", loud, cfg.MaxLoudnessDBFS)))
		}
		if cfg.MinLoudnessDBFS != 0 && loud < cfg.MinLoudnessDBFS && !f.Fill {
			rep.Faults = append(rep.Faults, verifyFault(f.Track, f.Sequence,
				fmt.Sprintf("loudness %.2f dBFS below %.2f", loud, cfg.MinLoudnessDBFS)))
		}
		if loud > rep.PeakLoudness {
			rep.PeakLoudness = loud
		}
		if f.Fill {
			rep.Fills++
		}
		rep.Frames++
		// Record the hash actually observed on disk; RunHash below is built
		// from these, never from the manifest's claims.
		verifiedFrames = append(verifiedFrames, artifact.Frame{SHA256: hashHex(data)})
		sr += stats.MeanR
		sg += stats.MeanG
		sb += stats.MeanB
		sv += stats.Variance
		sizeSum += int64(len(data))
	}
	if rep.Frames > 0 {
		n := float64(rep.Frames)
		rep.MeanR, rep.MeanG, rep.MeanB, rep.Variance = sr/n, sg/n, sb/n, sv/n
	}

	verifiedSegments, segFaults := verifySegments(outDir, m, &sizeSum, rep)
	rep.Faults = append(rep.Faults, segFaults...)

	alignFaults := checkTiming(m)
	rep.Faults = append(rep.Faults, alignFaults...)
	// Re-derive alignment from the files, not from the renderer's report: the
	// segment's stts timeline recovers each sample's position, and the frame
	// manifest supplies the intended PTS. A mismatch beyond the envelope is a
	// hard fault.
	recovered, alignFaults2 := checkSegmentAlignment(outDir, m, cfg)
	rep.MaxAlignNS = recovered
	rep.Faults = append(rep.Faults, alignFaults2...)
	if m.Summary.MaxAlignErrorNS != 0 && m.Summary.MaxAlignErrorNS < recovered {
		rep.Faults = append(rep.Faults, verifyFault(0, 0,
			fmt.Sprintf("recorded max align error %d ns is below re-derived %d ns",
				m.Summary.MaxAlignErrorNS, recovered)))
	}
	// Cross-check the recorded summary against re-derived values; a mismatch
	// means the manifest lied or the artefacts changed underneath it.
	if len(rep.Faults) == 0 {
		if err := checkSummary(m, rep, sizeSum, cfg); err != nil {
			rep.Faults = append(rep.Faults, verifyFault(0, 0, err.Error()))
		}
	}
	rep.TotalBytes = sizeSum

	if rep.TotalBytes > cfg.MaxBytes && cfg.MaxBytes > 0 {
		rep.Faults = append(rep.Faults, verifyFault(0, 0,
			fmt.Sprintf("total artefact bytes %d exceed the %d cap", rep.TotalBytes, cfg.MaxBytes)))
	}
	rep.RunHash = artifact.RunHash(verifiedFrames, verifiedSegments)
	if m.Summary.RunHash != "" && m.Summary.RunHash != rep.RunHash {
		rep.Faults = append(rep.Faults, verifyFault(0, 0,
			fmt.Sprintf("recorded run hash %s does not match re-derived %s",
				short(m.Summary.RunHash), short(rep.RunHash))))
	}
	// One stable sort after every fault has been appended, so the report order
	// never depends on which check happened to fire.
	rep.Faults.Sort()
	return rep, nil
}

func verifySegments(outDir string, m *artifact.Manifest, sizeSum *int64, rep *Report) ([]artifact.Segment, model.Faults) {
	var faults model.Faults
	var verified []artifact.Segment
	tracks := make(map[model.TrackID]bool)
	for _, s := range m.Segments {
		if tracks[s.Track] {
			faults = append(faults, verifyFault(s.Track, 0,
				fmt.Sprintf("duplicate segment for track %d", s.Track)))
			continue
		}
		tracks[s.Track] = true
		if err := safeJoin(outDir, s.Path); err != nil {
			faults = append(faults, verifyFault(s.Track, 0, err.Error()))
			continue
		}
		f, err := os.Open(filepath.Join(outDir, s.Path))
		if err != nil {
			faults = append(faults, verifyFault(s.Track, 0,
				fmt.Sprintf("missing segment: %v", err)))
			continue
		}
		meta, payloads, err := mp4seg.Read(f)
		f.Close()
		if err != nil {
			faults = append(faults, verifyFault(s.Track, 0,
				fmt.Sprintf("parse segment: %v", err)))
			continue
		}
		st, err := os.Stat(filepath.Join(outDir, s.Path))
		if err != nil {
			faults = append(faults, verifyFault(s.Track, 0, err.Error()))
			continue
		}
		got := hashFileHex(filepath.Join(outDir, s.Path))
		if got != s.SHA256 {
			faults = append(faults, verifyFault(s.Track, 0,
				fmt.Sprintf("segment hash %s does not match recorded %s", short(got), short(s.SHA256))))
		}
		verified = append(verified, artifact.Segment{SHA256: got})
		if st.Size() != s.Bytes {
			faults = append(faults, verifyFault(s.Track, 0,
				fmt.Sprintf("segment size %d does not match recorded %d", st.Size(), s.Bytes)))
		}
		if meta.Width != s.Width || meta.Height != s.Height {
			faults = append(faults, verifyFault(s.Track, 0,
				fmt.Sprintf("segment geometry %dx%d does not match recorded %dx%d",
					meta.Width, meta.Height, s.Width, s.Height)))
		}
		if len(payloads) != s.Samples || len(meta.Deltas) != s.Samples {
			faults = append(faults, verifyFault(s.Track, 0,
				fmt.Sprintf("segment sample count %d/%d does not match recorded %d",
					len(payloads), len(meta.Deltas), s.Samples)))
		}
		if s.Samples > 0 {
			reconstructed := reconstructPTS(meta, payloads, m, s.Track)
			if len(reconstructed) > 0 {
				if first, last := reconstructed[0], reconstructed[len(reconstructed)-1]; first != s.FirstPTS || last != s.LastPTS {
					faults = append(faults, verifyFault(s.Track, 0,
						fmt.Sprintf("segment timing %d..%d does not match recorded %d..%d",
							first, last, s.FirstPTS, s.LastPTS)))
				}
			}
			faults = append(faults, crossCheckPayloads(outDir, s.Track, payloads, m)...)
		}
		rep.Segments++
		*sizeSum += st.Size()
	}
	return verified, faults
}

// crossCheckPayloads proves the segment samples are the very frame files the
// manifest lists: each mdat payload must byte-match a frame PNG of the same
// track. This catches artefacts that were edited after export even when their
// recorded hash was rewritten to match.
func crossCheckPayloads(outDir string, track model.TrackID, payloads [][]byte, m *artifact.Manifest) model.Faults {
	byHash := make(map[string]artifact.Frame, len(m.Frames))
	for _, f := range m.Frames {
		if f.Track == track {
			byHash[f.SHA256] = f
		}
	}
	var faults model.Faults
	for i, p := range payloads {
		f, ok := byHash[hashHex(p)]
		if !ok {
			faults = append(faults, verifyFault(track, uint32(i+1),
				fmt.Sprintf("segment sample %d does not match any recorded frame", i)))
			continue
		}
		data, err := os.ReadFile(filepath.Join(outDir, f.Path))
		if err != nil {
			faults = append(faults, verifyFault(track, f.Sequence,
				fmt.Sprintf("frame file missing for segment sample: %v", err)))
			continue
		}
		if hashHex(data) != f.SHA256 {
			faults = append(faults, verifyFault(track, f.Sequence,
				"frame file no longer matches the segment sample"))
		}
	}
	return faults
}

// reconstructPTS walks stts deltas from the recorded first PTS and returns the
// resulting timeline. The first PTS is verified against the frame records, so
// the reconstruction is anchored in a claim that is itself cross-checked.
func reconstructPTS(meta mp4seg.TrackMeta, payloads [][]byte, m *artifact.Manifest, track model.TrackID) []int64 {
	byHash := make(map[string]int64, len(m.Frames))
	for _, f := range m.Frames {
		if f.Track == track {
			byHash[f.SHA256] = f.PTS
		}
	}
	var first int64
	found := false
	for _, p := range payloads {
		if pts, ok := byHash[hashHex(p)]; ok {
			first = pts
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	out := make([]int64, len(payloads))
	out[0] = first
	for i := 1; i < len(out); i++ {
		if i < len(meta.Deltas) {
			out[i] = out[i-1] + int64(meta.Deltas[i])
		}
	}
	return out
}

func checkGeometry(raw pixel.Raw, f artifact.Frame, cfg Config) error {
	if raw.W != f.Width || raw.H != f.Height {
		return fmt.Errorf("decoded %dx%d does not match recorded %dx%d", raw.W, raw.H, f.Width, f.Height)
	}
	if cfg.MaxDimension > 0 && (raw.W > cfg.MaxDimension || raw.H > cfg.MaxDimension) {
		return fmt.Errorf("geometry %dx%d exceeds the %d cap", raw.W, raw.H, cfg.MaxDimension)
	}
	if err := raw.Validate(); err != nil {
		return err
	}
	return nil
}

func checkStats(raw pixel.Raw, f artifact.Frame, cfg Config) error {
	stats, err := pixel.Measure(raw)
	if err != nil {
		return err
	}
	tol := cfg.VarianceTolerance
	if tol <= 0 {
		tol = 1e-6
	}
	if absDiff(stats.MeanR, f.Stats.MeanR) > 1e-3 ||
		absDiff(stats.MeanG, f.Stats.MeanG) > 1e-3 ||
		absDiff(stats.MeanB, f.Stats.MeanB) > 1e-3 ||
		absDiff(stats.Variance, f.Stats.Variance) > tol*max(1, f.Stats.Variance) {
		return fmt.Errorf("pixel statistics drifted from the recorded values")
	}
	if cfg.MaxVariance > 0 && stats.Variance > cfg.MaxVariance {
		return fmt.Errorf("variance %.3f exceeds %.3f", stats.Variance, cfg.MaxVariance)
	}
	return nil
}

// checkSegmentAlignment re-derives the largest timing error between the frame
// manifest's PTS and the positions implied by each segment's stts timeline.
// This reads the container afresh, so a renderer that merely claims a small
// alignment error cannot pass.
func checkSegmentAlignment(outDir string, m *artifact.Manifest, cfg Config) (int64, model.Faults) {
	var worst int64
	var faults model.Faults
	for _, s := range m.Segments {
		f, err := os.Open(filepath.Join(outDir, s.Path))
		if err != nil {
			continue
		}
		meta, payloads, err := mp4seg.Read(f)
		f.Close()
		if err != nil || len(meta.Deltas) != len(payloads) {
			continue
		}
		byHash := make(map[string]int64, len(m.Frames))
		for _, fr := range m.Frames {
			if fr.Track == s.Track {
				byHash[fr.SHA256] = fr.PTS
			}
		}
		var declared []int64
		for _, p := range payloads {
			if pts, ok := byHash[hashHex(p)]; ok {
				declared = append(declared, pts)
			}
		}
		if len(declared) == 0 {
			continue
		}
		// Sample 0's stts delta is synthesised by the writer (it equals sample
		// 1's), so the container position of sample i accumulates the real
		// deltas 1..i. Compare that against the declared PTS relative to the
		// first sample. Sampling (fps) legitimately makes gaps larger than the
		// nominal cadence, so only the accumulated difference matters, not the
		// individual gap.
		base := declared[0]
		var container int64
		for i := 1; i < len(declared); i++ {
			container += int64(meta.Deltas[i])
			drift := abs64((declared[i] - base) - container)
			if drift > worst {
				worst = drift
			}
			if cfg.MaxAlignErrorNS > 0 && drift > cfg.MaxAlignErrorNS {
				faults = append(faults, verifyFault(s.Track, uint32(i+1),
					fmt.Sprintf("segment sample %d drifts %d ns from its declared PTS", i, drift)))
			}
		}
	}
	return worst, faults
}

// checkTiming validates that each track's frame timestamps are strictly
// increasing. Gaps are not compared against the alignment envelope: the
// exported frame set is sampled (fps) and can contain recoverable gaps, so a
// wide gap is normal. The envelope governs error against the intended PTS,
// which checkSegmentAlignment measures.
func checkTiming(m *artifact.Manifest) model.Faults {
	var faults model.Faults
	byTrack := make(map[model.TrackID][]artifact.Frame)
	for _, f := range m.Frames {
		byTrack[f.Track] = append(byTrack[f.Track], f)
	}
	for tr, frames := range byTrack {
		sort.SliceStable(frames, func(i, j int) bool { return frames[i].PTS < frames[j].PTS })
		for i := 1; i < len(frames); i++ {
			if frames[i].PTS <= frames[i-1].PTS {
				faults = append(faults, verifyFault(tr, frames[i].Sequence,
					fmt.Sprintf("frame %d PTS %d does not advance past frame %d PTS %d",
						frames[i].Sequence, frames[i].PTS,
						frames[i-1].Sequence, frames[i-1].PTS)))
			}
		}
	}
	return faults
}

func checkSummary(m *artifact.Manifest, rep *Report, sizeSum int64, cfg Config) error {
	if m.Summary.Frames != rep.Frames {
		return fmt.Errorf("manifest summary frame count %d != verified %d", m.Summary.Frames, rep.Frames)
	}
	if m.Summary.TotalBytes != sizeSum {
		return fmt.Errorf("manifest summary bytes %d != verified %d", m.Summary.TotalBytes, sizeSum)
	}
	if m.Summary.PeakLoudnessDBFS > cfg.MaxLoudnessDBFS && cfg.MaxLoudnessDBFS > 0 {
		return fmt.Errorf("peak loudness %.2f dBFS exceeds cap", m.Summary.PeakLoudnessDBFS)
	}
	if absDiff(m.Summary.Variance, rep.Variance) > 1e-3*max(1, rep.Variance) {
		return fmt.Errorf("manifest variance %.6f != verified %.6f", m.Summary.Variance, rep.Variance)
	}
	return nil
}

func safeJoin(root, rel string) error {
	if rel == "" {
		return fmt.Errorf("empty artefact path")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("absolute artefact path %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || len(clean) > 2 && clean[:3] == "../" {
		return fmt.Errorf("artefact path %q escapes the output directory", rel)
	}
	return nil
}

func verifyFault(track model.TrackID, frame uint32, msg string) *model.Fault {
	return model.NewFault(model.KindVerify, model.CodeConstraintViolation, track, frame, msg)
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hashFileHex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashHex(data)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func absDiff(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
