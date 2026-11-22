package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pairwise/mediad/internal/checkpoint"
	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/pipeline"
	"github.com/pairwise/mediad/internal/verify"
)

// verify re-runs the invariant checks and hash recomputation for a directory
// produced by demo. The recorded continuous/resumed manifest hashes are
// re-derived from the saved state to catch tampering between runs.
func verifyDir(dir string) (verify.Summary, error) {
	manifestPath := filepath.Join(dir, "manifest.json")
	m, err := verify.Load(manifestPath)
	if err != nil {
		return verify.Summary{}, err
	}
	sum, err := verify.Run(m, filepath.Join(dir, "media"), verify.Options{AcceptRecovered: m.Demo})
	if err != nil {
		return sum, err
	}
	if !sum.ResumeIdentical {
		return sum, fault.Errorf("verify.resume", fault.KindVerify,
			"continuous and resumed manifests are missing or differ").Fatal()
	}
	// Replaying the saved resumed checkpoint must reproduce the recorded
	// manifest hash, proving checkpoint durability across process boundaries.
	if err := recheckResume(dir, m); err != nil {
		return sum, err
	}
	return sum, nil
}

// recheckResume replays the saved checkpoint of the interrupted run and
// re-derives the resumed manifest from media, then compares it with the
// continuous manifest hash recorded at demo time. This catches checkpoint
// loss or mutation between the demo and verification.
func recheckResume(dir string, m *verify.Manifest) error {
	if m.Summary.ContinuousHash == "" || m.Summary.ResumedHash == "" {
		return fault.Errorf("verify.resume", fault.KindVerify, "manifest lacks continuous or resumed hash").Fatal()
	}
	if m.Summary.ContinuousHash != m.Summary.ResumedHash {
		return fault.Errorf("verify.resume", fault.KindVerify,
			"continuous %s and resumed %s hashes differ", m.Summary.ContinuousHash, m.Summary.ResumedHash).Fatal()
	}
	stateDir := filepath.Join(dir, "state", "resumed")
	st, err := checkpoint.Open(stateDir)
	if err != nil {
		return fault.New("verify.resume", fault.KindVerify, err).WithPath(stateDir)
	}
	defer st.Close()
	if st.NextBlock() == 0 {
		return fault.Errorf("verify.resume", fault.KindVerify, "resumed checkpoint has no committed blocks").WithPath(stateDir).Fatal()
	}
	mediaDir := filepath.Join(dir, "media")
	video := resumeVideo(m)
	if video == "" {
		return fault.Errorf("verify.resume", fault.KindVerify, "no video file recorded for the resumed run").Fatal()
	}
	sess, err := pipeline.OpenSession(filepath.Join(mediaDir, video))
	if err != nil {
		return fault.New("verify.resume", fault.KindVerify, err).WithPath(video)
	}
	defer sess.Close()
	planner, err := sess.Planner()
	if err != nil {
		return fault.New("verify.resume", fault.KindVerify, err).WithPath(video)
	}
	reqs := sess.ChapterRequests(3)
	if len(reqs) == 0 {
		return fault.Errorf("verify.resume", fault.KindVerify, "resumed run has no requests").Fatal()
	}
	res, err := pipeline.RunBatch(planner, reqs, st, pipeline.BatchOptions{SamplesPerBlock: 120})
	if err != nil && res == nil {
		return fault.New("verify.resume", fault.KindVerify, err).WithPath(video)
	}
	if res.ManifestSHA256 != m.Summary.ResumedHash {
		return fault.Errorf("verify.resume", fault.KindVerify,
			"replayed manifest %s != recorded %s", res.ManifestSHA256, m.Summary.ResumedHash).Fatal()
	}
	fmt.Printf("resumed manifest hash replay verified: %s\n", res.ManifestSHA256)
	return nil
}

// resumeVideo returns the file used for the batch comparison.
func resumeVideo(m *verify.Manifest) string {
	for _, f := range m.Files {
		if !f.Damaged && strings.Contains(f.Name, "cam_a") {
			return f.Name
		}
	}
	for _, f := range m.Files {
		if !f.Damaged {
			return f.Name
		}
	}
	return ""
}
