// Package verify checks cross-stage invariants and re-derives manifests from
// media, so a resumed run can be proven identical to a continuous one and a
// recorded segment hash can be proven to match the bytes it names.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/fingerprint"
	"github.com/pairwise/mediad/internal/pipeline"
)

// Minimums for the acceptance thresholds.
const (
	MinRoundtripRate   = 0.98
	MinKeyframeHitRate = 0.95
)

// FileEntry records one analysed input.
type FileEntry struct {
	Name     string `json:"name"`
	RelPath  string `json:"rel_path"`
	Damaged  bool   `json:"damaged"`
	Warnings int    `json:"warnings"`
	Bytes    int64  `json:"bytes"`
}

// SegmentEntry is one delivered slice.
type SegmentEntry struct {
	ID           string `json:"id"`
	File         string `json:"file"`
	TrackID      uint32 `json:"track_id"`
	StartNS      int64  `json:"start_ns"`
	EndNS        int64  `json:"end_ns"`
	FirstSample  int    `json:"first_sample"`
	SampleCount  int    `json:"sample_count"`
	KeyOffset    int64  `json:"key_offset"`
	KeyExact     bool   `json:"key_exact"`
	Inferred     bool   `json:"inferred"`
	SilentNS     int64  `json:"silent_ns"`
	SilenceKnown bool   `json:"silence_known"`
	SHA256       string `json:"sha256"`
	Recovered    bool   `json:"recovered"`
	DuplicateOf  string `json:"duplicate_of,omitempty"`
	Collision    bool   `json:"collision,omitempty"`
}

// Summary carries the acceptance fields printed by demo and verify.
type Summary struct {
	RoundtripRate     float64 `json:"roundtrip_rate"`
	KeyframeHitRate   float64 `json:"keyframe_hit_rate"`
	Segments          int     `json:"segments"`
	RecoveredSegments int     `json:"recovered_segments"`
	DuplicateSegments int     `json:"duplicate_segments"`
	SegmentSHA256     string  `json:"segment_sha256"`
	ContinuousHash    string  `json:"continuous_manifest_sha256"`
	ResumedHash       string  `json:"resumed_manifest_sha256"`
	ResumeIdentical   bool    `json:"resume_identical"`
}

// Manifest is the demo/verify interchange document.
type Manifest struct {
	Version int `json:"version"`
	// Demo marks a self-generated corpus whose damage is intentional. Only a
	// demo manifest may exit 0 with recovered slices; an operator directory
	// with the same damage reports it as partially recovered (exit 1).
	Demo     bool           `json:"demo"`
	Files    []FileEntry    `json:"files"`
	Segments []SegmentEntry `json:"segments"`
	Summary  Summary        `json:"summary"`
}

// Save writes a manifest atomically.
func Save(path string, m *Manifest) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fault.New("verify.save", fault.KindVerify, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fault.New("verify.save", fault.KindIO, err).WithPath(tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fault.New("verify.save", fault.KindIO, err).WithPath(path)
	}
	return nil
}

// Load reads a manifest and rejects malformed documents as verification
// failures (exit 2).
func Load(path string) (*Manifest, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.New("verify.load", fault.KindVerify, err).WithPath(path)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fault.New("verify.load", fault.KindVerify, err).WithPath(path)
	}
	if m.Version != 1 {
		return nil, fault.Errorf("verify.load", fault.KindVerify, "unsupported manifest version %d", m.Version).WithPath(path)
	}
	return &m, nil
}

// Check runs every structural invariant. It returns a verify error (exit 2)
// on any violation.
func Check(m *Manifest) error {
	var errs []error
	if len(m.Files) == 0 || len(m.Segments) == 0 {
		errs = append(errs, fault.Errorf("verify.check", fault.KindVerify, "manifest has no files or segments").Fatal())
	}
	byID := map[string]SegmentEntry{}
	for i, s := range m.Segments {
		if s.EndNS <= s.StartNS {
			errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
				"segment %d %q has empty extent [%d,%d)", i, s.ID, s.StartNS, s.EndNS).Fatal())
		}
		if s.EndNS-s.StartNS > pipeline.MaxSegmentNS {
			errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
				"segment %q spans %d ns, above the 30 minute cap", s.ID, s.EndNS-s.StartNS).Fatal())
		}
		if s.SampleCount <= 0 {
			errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
				"segment %q has %d samples", s.ID, s.SampleCount).Fatal())
		}
		if s.SHA256 == "" || len(s.SHA256) != 64 {
			errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
				"segment %q has malformed sha256 %q", s.ID, s.SHA256).Fatal())
		}
		if s.SilentNS < 0 || s.SilentNS > s.EndNS-s.StartNS {
			errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
				"segment %q silence %d outside its extent", s.ID, s.SilentNS).Fatal())
		}
		if !s.SilenceKnown && s.SilentNS != 0 {
			errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
				"segment %q claims silence on an undetectable track", s.ID).Fatal())
		}
		if prev, ok := byID[s.ID]; ok {
			errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
				"duplicate segment id %q ([%d,%d) and [%d,%d))", s.ID, prev.StartNS, prev.EndNS, s.StartNS, s.EndNS).Fatal())
		}
		byID[s.ID] = s
		if s.DuplicateOf != "" {
			original, ok := byID[s.DuplicateOf]
			if s.DuplicateOf == s.ID {
				errs = append(errs, fault.Errorf("verify.check", fault.KindVerify, "segment %q deduplicated against itself", s.ID).Fatal())
			} else if !ok {
				errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
					"segment %q cites unknown original %q", s.ID, s.DuplicateOf).Fatal())
			} else if original.SHA256 != s.SHA256 || original.TrackID != s.TrackID ||
				original.StartNS != s.StartNS || original.EndNS != s.EndNS {
				errs = append(errs, fault.Errorf("verify.check", fault.KindVerify,
					"segment %q is marked duplicate of %q but extent or hash differs", s.ID, s.DuplicateOf).Fatal())
			}
			continue
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if got := KeyframeHitRate(m.Segments); got < MinKeyframeHitRate {
		return fault.Errorf("verify.check", fault.KindVerify,
			"keyframe_hit_rate %.4f below %.2f", got, MinKeyframeHitRate).Fatal()
	}
	return nil
}

// KeyframeHitRate is the share of segments whose cut landed exactly on a sync
// frame. Inferred cuts never count as hits.
func KeyframeHitRate(segs []SegmentEntry) float64 {
	if len(segs) == 0 {
		return 0
	}
	var hits int
	for _, s := range segs {
		if s.KeyExact {
			hits++
		}
	}
	return float64(hits) / float64(len(segs))
}

// Recompute re-reads every segment's bytes and recomputes its SHA-256,
// returning the round-trip rate and any mismatches.
func Recompute(m *Manifest, mediaDir string) (float64, []error) {
	if len(m.Segments) == 0 {
		return 0, []error{fault.Errorf("verify.recompute", fault.KindVerify, "no segments to recompute").Fatal()}
	}
	sessions := map[string]*pipeline.Session{}
	var errs []error
	var ok int
	defer func() {
		for _, s := range sessions {
			s.Close()
		}
	}()
	for _, seg := range m.Segments {
		path := filepath.Join(mediaDir, seg.File)
		sess, seen := sessions[path]
		if !seen {
			var err error
			sess, err = pipeline.OpenSession(path)
			if err != nil {
				errs = append(errs, fault.New("verify.recompute", fault.KindVerify, err).WithPath(path))
				continue
			}
			sessions[path] = sess
			if sess.Err != nil {
				// Damage is expected for the corrupted fixtures; it must not
				// prevent recomputation of verified segments.
				_ = sess.Err
			}
		}
		src := pipeline.NewSampleSource(sess.Tree, sess.Movie)
		digest, _, err := fingerprint.Sum(src, seg.TrackID, seg.FirstSample, seg.SampleCount)
		if err != nil {
			errs = append(errs, fault.New("verify.recompute", fault.KindVerify, err).
				WithPath(path).WithTrack(seg.TrackID).WithSample(seg.FirstSample))
			continue
		}
		got := hex.EncodeToString(digest[:])
		if got != seg.SHA256 {
			errs = append(errs, fault.Errorf("verify.recompute", fault.KindVerify,
				"segment %q hash %s != recorded %s", seg.ID, got, seg.SHA256).
				WithPath(path).WithTrack(seg.TrackID).WithSample(seg.FirstSample))
			continue
		}
		ok++
	}
	rate := float64(ok) / float64(len(m.Segments))
	if len(errs) > 0 {
		return rate, errs
	}
	return rate, nil
}

// SegmentDigest folds every segment hash into the summary's segment_sha256.
func SegmentDigest(segs []SegmentEntry) string {
	cp := append([]SegmentEntry(nil), segs...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].ID < cp[j].ID })
	h := sha256.New()
	for _, s := range cp {
		fmt.Fprintf(h, "%s|%s|%d|%d|%d|%s\n", s.ID, s.File, s.TrackID, s.StartNS, s.EndNS, s.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Run verifies a manifest and returns a summary. The error's exit code
// distinguishes recoverable damage (1) from failed verification (2).
func Run(m *Manifest, mediaDir string, opts Options) (Summary, error) {
	sum := m.Summary
	if computed := len(m.Segments) - sum.DuplicateSegments; computed >= 0 {
		sum.Segments = computed
	}
	sum.KeyframeHitRate = KeyframeHitRate(m.Segments)
	rate, errs := Recompute(m, mediaDir)
	sum.RoundtripRate = rate
	digest := SegmentDigest(m.Segments)
	if sum.SegmentSHA256 != "" && sum.SegmentSHA256 != digest {
		errs = append(errs, fault.Errorf("verify.run", fault.KindVerify,
			"segment digest %s != recorded %s", digest, sum.SegmentSHA256).Fatal())
	}
	sum.SegmentSHA256 = digest
	if err := Check(m); err != nil {
		errs = append(errs, err)
	}
	if rate < MinRoundtripRate {
		errs = append(errs, fault.Errorf("verify.run", fault.KindVerify,
			"roundtrip_rate %.4f below %.2f", rate, MinRoundtripRate).Fatal())
	}
	if sum.ContinuousHash != "" && sum.ResumedHash != "" && sum.ContinuousHash != sum.ResumedHash {
		errs = append(errs, fault.Errorf("verify.run", fault.KindVerify,
			"resumed manifest %s differs from continuous %s", sum.ResumedHash, sum.ContinuousHash).Fatal())
	}
	sum.ResumeIdentical = sum.ContinuousHash != "" && sum.ContinuousHash == sum.ResumedHash
	if len(errs) > 0 {
		return sum, errors.Join(errs...)
	}
	if sum.RecoveredSegments > 0 && !opts.AcceptRecovered {
		return sum, fault.Errorf("verify.run", fault.KindTruncated,
			"%d segments recovered from damaged input", sum.RecoveredSegments).Recovered()
	}
	return sum, nil
}

// Options tunes verification strictness.
type Options struct {
	// AcceptRecovered lets a demo that deliberately corrupts input exit 0
	// while the same damage in an operator-supplied directory still reports
	// partially recovered results (exit 1).
	AcceptRecovered bool
}
