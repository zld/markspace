// Package validate independently re-reads products and source media to
// confirm hashes, pixels, loudness, counts and alignment.
package validate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"pwflow/internal/ebml"
	"pwflow/internal/fault"
	"pwflow/internal/fingerprint"
	"pwflow/internal/pipeline"
	pwrtp "pwflow/internal/rtp"
)

// Check is one named verification result.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report is the independent verification result.
type Report struct {
	ManifestPath string             `json:"manifest_path"`
	ManifestSHA  string             `json:"manifest_sha256"`
	Status       string             `json:"status"`
	Checks       []Check            `json:"checks"`
	Failures     []string           `json:"failures,omitempty"`
	Manifest     *pipeline.Manifest `json:"-"`
}

// Verify reads manifestPath and re-decodes/re-hashes the products.
func Verify(ctx context.Context, manifestPath string) (*Report, error) {
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fault.Wrap(fault.InvalidArgument, err)
	}
	var m pipeline.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fault.Wrap(fault.VerificationFailed, err)
	}
	sha := fingerprint.Bytes(b)
	rep := &Report{ManifestPath: manifestPath, ManifestSHA: sha, Status: m.Status, Manifest: &m}
	manifestDir := filepath.Dir(manifestPath)

	for _, s := range m.Segments {
		path := filepath.Join(manifestDir, s.File)
		got, size, err := fingerprint.File(path)
		if err != nil {
			rep.fail("segment-file:"+s.File, "missing/unreadable: "+err.Error())
			continue
		}
		if got != s.FileSHA256 || size != s.FileSize {
			rep.fail("segment-hash:"+s.File, fmt.Sprintf("hash/size mismatch got=%s/%d want=%s/%d", got, size, s.FileSHA256, s.FileSize))
		} else {
			rep.pass("segment-hash:"+s.File, got)
		}
		root := hashFrameRoot(s.Frames)
		if root != s.FramesSHA256 {
			rep.fail("frame-root:"+s.File, "frame hash chain mismatch")
		}
	}

	for role, want := range map[string]string{"media": m.Inputs.MediaSHA, "events": m.Inputs.EventsSHA, "rtp": m.Inputs.RTPSHA} {
		path := map[string]string{"media": m.Inputs.MediaPath, "events": m.Inputs.EventsPath, "rtp": m.Inputs.RTPPath}[role]
		got, _, err := fingerprint.File(path)
		if err != nil || got != want {
			rep.fail("input-digest:"+role, fmt.Sprintf("got=%s err=%v want=%s", got, err, want))
		} else {
			rep.pass("input-digest:"+role, got)
		}
	}

	mediaBytes, err := os.ReadFile(m.Inputs.MediaPath)
	if err != nil {
		rep.fail("ebml-reread", err.Error())
	} else if ebmlRep, err := ebml.Parse(mediaBytes, ebml.DefaultLimits()); err != nil {
		rep.fail("ebml-reread", err.Error())
	} else {
		rep.pass("ebml-reread", fmt.Sprintf("blocks=%d damages=%d", len(ebmlRep.Blocks), len(ebmlRep.Damages)))
		if len(ebmlRep.Damages) != m.Counts.EBMLDamages {
			rep.fail("ebml-damage-count", fmt.Sprintf("manifest=%d reparsed=%d", m.Counts.EBMLDamages, len(ebmlRep.Damages)))
		}
		checkAlignment(rep, &m, ebmlRep)
		w, h := videoDims(ebmlRep)
		checkVideo(ctx, rep, &m, m.Inputs.MediaPath, w, h)
	}

	checkRTP(rep, &m)
	checkAudio(ctx, rep, &m)
	checkCountsAndStatus(rep, &m)

	if len(rep.Failures) > 0 {
		rep.Status = pipeline.StatusFailed
		return rep, fault.New(fault.VerificationFailed, "%d verification check(s) failed", len(rep.Failures))
	}
	if m.Status == pipeline.StatusGaps {
		rep.Status = pipeline.StatusGaps
	} else {
		rep.Status = pipeline.StatusPass
	}
	return rep, nil
}

func (r *Report) pass(name, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, OK: true, Detail: detail})
}

func (r *Report) fail(name, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, OK: false, Detail: detail})
	r.Failures = append(r.Failures, name+": "+detail)
}

func hashFrameRoot(frames []pipeline.FrameInfo) string {
	h := newSHA()
	for _, f := range frames {
		h.Write([]byte(f.SHA256))
		h.Write([]byte{'\n'})
	}
	return hexSum(h)
}

func checkCountsAndStatus(rep *Report, m *pipeline.Manifest) {
	if m.Counts.Segments != len(m.Segments) {
		rep.fail("count-segments", "count does not match segment array")
	}
	if m.Counts.SilenceIntervals != len(m.Silences) {
		rep.fail("count-silence", "count does not match silence array")
	}
	if m.Counts.MissingPackets == 0 && m.Status == pipeline.StatusGaps && len(m.EBMLDamages) == 0 {
		rep.fail("status", "PASS_WITH_GAPS without gaps or EBML damage")
	}
	if math.Abs(m.Alignment.MaxErrorMS) > 40.000001 {
		rep.fail("alignment-threshold", fmt.Sprintf("max alignment %.3f ms > 40 ms", m.Alignment.MaxErrorMS))
	} else {
		rep.pass("alignment-threshold", fmt.Sprintf("%.3f ms", m.Alignment.MaxErrorMS))
	}
}

func checkRTP(rep *Report, m *pipeline.Manifest) {
	f, err := os.Open(m.Inputs.RTPPath)
	if err != nil {
		rep.fail("rtp-reread", err.Error())
		return
	}
	defer f.Close()
	records, err := pwrtp.ReadTrace(f)
	if err != nil {
		rep.fail("rtp-reread", err.Error())
		return
	}
	r := pwrtp.NewRepairer(64)
	for _, rec := range records {
		r.Add(rec)
	}
	got := r.Flush()
	if got.Duplicates != m.Counts.DuplicatePackets || got.Missing != m.Counts.MissingPackets {
		rep.fail("rtp-counts", fmt.Sprintf("manifest dup=%d miss=%d reparsed dup=%d miss=%d",
			m.Counts.DuplicatePackets, m.Counts.MissingPackets, got.Duplicates, got.Missing))
	} else {
		rep.pass("rtp-counts", fmt.Sprintf("records=%d dup=%d miss=%d", got.Records, got.Duplicates, got.Missing))
	}
}

func videoDims(r *ebml.Report) (int, int) {
	for _, t := range r.Tracks {
		if t.Type == 1 && t.Width > 0 && t.Height > 0 {
			return int(t.Width), int(t.Height)
		}
	}
	return 320, 240
}
