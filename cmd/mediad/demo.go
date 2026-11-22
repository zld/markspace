package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pairwise/mediad/internal/checkpoint"
	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/fixtures"
	"github.com/pairwise/mediad/internal/pipeline"
	"github.com/pairwise/mediad/internal/verify"
)

// demo builds the corpus, runs a continuous pass and an interrupted-then-
// resumed pass, verifies both, and writes manifest.json.
func demo(out string, fresh bool) (verify.Summary, error) {
	if fresh {
		if err := os.RemoveAll(out); err != nil {
			return verify.Summary{}, fault.New("demo.clean", fault.KindIO, err).WithPath(out)
		}
	}
	mediaDir := filepath.Join(out, "media")
	files, err := fixtures.WriteDemo(mediaDir)
	if err != nil {
		return verify.Summary{}, fault.New("demo.fixtures", fault.KindIO, err).WithPath(mediaDir)
	}
	fmt.Printf("generated %d media files in %s\n", len(files), mediaDir)

	manifest, err := buildManifest(mediaDir, files, out)
	if err != nil {
		return manifest.Summary, err
	}
	// Verify before publishing so the stored summary carries the measured
	// round-trip and keyframe rates, not placeholder zeroes.
	sum, runErr := verify.Run(manifest, mediaDir, verify.Options{AcceptRecovered: true})
	manifest.Summary = sum
	manifestPath := filepath.Join(out, "manifest.json")
	if err := verify.Save(manifestPath, manifest); err != nil {
		return sum, err
	}
	fmt.Printf("manifest: %s\n", manifestPath)
	if runErr != nil {
		return sum, runErr
	}
	return sum, nil
}

// buildManifest analyses every file, executes the continuous and resumed
// batch runs, and folds the results into the manifest document.
func buildManifest(mediaDir string, files []fixtures.DemoFile, out string) (*verify.Manifest, error) {
	m := &verify.Manifest{Version: 1, Demo: true}
	byName := map[string]fixtures.DemoFile{}
	for _, f := range files {
		byName[f.Name] = f
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	sort.Strings(names)

	var problems []error
	var duplicate int
	for _, name := range names {
		path := filepath.Join(mediaDir, name)
		sess, err := pipeline.OpenSession(path)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		damaged := byName[name].Damaged
		if sess.Err != nil {
			problems = append(problems, sess.Err)
		}
		st, stErr := os.Stat(path)
		if stErr != nil {
			sess.Close()
			return nil, fault.New("demo.stat", fault.KindIO, stErr).WithPath(path)
		}
		m.Files = append(m.Files, verify.FileEntry{
			Name: name, RelPath: filepath.Join("media", name), Damaged: damaged,
			Warnings: len(sess.Warnings), Bytes: st.Size(),
		})
		segs, err := analyseFile(sess, path, name)
		if err != nil && len(segs) == 0 {
			sess.Close()
			problems = append(problems, err)
			continue
		}
		sess.Close()
		m.Segments = append(m.Segments, segs...)
	}

	if len(m.Segments) == 0 {
		return nil, fault.Errorf("demo.manifest", fault.KindIndex, "no segments produced").Fatal()
	}
	markDuplicates(m.Segments, &duplicate)
	if err := runBatchComparison(mediaDir, names, m, out); err != nil {
		problems = append(problems, err)
	}
	m.Summary.DuplicateSegments = duplicate
	m.Summary.RecoveredSegments = countRecovered(m.Segments)
	m.Summary.Segments = len(m.Segments) - duplicate
	m.Summary.SegmentSHA256 = verify.SegmentDigest(m.Segments)
	m.Summary.KeyframeHitRate = verify.KeyframeHitRate(m.Segments)
	if len(problems) > 0 && m.Summary.RecoveredSegments == 0 {
		// Damage that produced no recoverable slice is an index failure.
		return nil, fault.New("demo.manifest", fault.KindIndex, errors.Join(problems...)).Fatal()
	}
	return m, nil
}

// analyseFile segments all video tracks of a session.
func analyseFile(sess *pipeline.Session, path, name string) ([]verify.SegmentEntry, error) {
	planner, err := sess.Planner()
	if err != nil {
		return nil, err
	}
	reqs := sess.ChapterRequests(3)
	res, runErr := planner.Run(reqs)
	out := make([]verify.SegmentEntry, 0, len(res.Segments))
	for _, s := range res.Segments {
		out = append(out, verify.SegmentEntry{
			ID: name + ":" + s.ID, File: name, TrackID: s.TrackID,
			StartNS: s.StartNS, EndNS: s.EndNS, FirstSample: s.FirstSample,
			SampleCount: s.SampleCount, KeyOffset: s.KeyOffset, KeyExact: s.KeyExact,
			Inferred: !s.KeyExact, SilentNS: s.SilentNS, SilenceKnown: s.SilenceKnown,
			SHA256: s.SHA256, Recovered: s.Recovered,
		})
	}
	return out, runErr
}

// markDuplicates reports repeated camera content. Identical extents plus
// identical payload are the primary evidence; when two files were recorded
// with a different edit anchor, the same payload appears at a shifted extent,
// and that is flagged only after the per-sample digests agree.
func markDuplicates(segs []verify.SegmentEntry, count *int) {
	byExtent := map[string]string{}
	byContent := map[string]string{}
	for i := range segs {
		if segs[i].Recovered {
			// Damaged sources are excluded: two cameras whose prefixes were
			// salvaged can share bytes coincidentally, and a duplicate claim
			// must never be made from partial data.
			continue
		}
		extentKey := fmt.Sprintf("%d|%d|%d|%s", segs[i].TrackID, segs[i].StartNS, segs[i].EndNS, segs[i].SHA256)
		if owner, ok := byExtent[extentKey]; ok && owner != segs[i].ID {
			segs[i].DuplicateOf = owner
			*count++
			continue
		}
		contentKey := fmt.Sprintf("%d|%d|%s", segs[i].TrackID, segs[i].SampleCount, segs[i].SHA256)
		if owner, ok := byContent[contentKey]; ok && owner != segs[i].ID {
			segs[i].DuplicateOf = owner
			*count++
			continue
		}
		if _, ok := byExtent[extentKey]; !ok {
			byExtent[extentKey] = segs[i].ID
		}
		if _, ok := byContent[contentKey]; !ok {
			byContent[contentKey] = segs[i].ID
		}
	}
}

func countRecovered(segs []verify.SegmentEntry) int {
	var n int
	for _, s := range segs {
		if s.Recovered {
			n++
		}
	}
	return n
}

// runBatchComparison drives the checkpointed batch twice: once continuously
// and once interrupted then resumed, then proves the manifests match.
func runBatchComparison(mediaDir string, names []string, m *verify.Manifest, out string) error {
	video := pickVideoFile(mediaDir, names)
	if video == "" {
		return nil
	}
	sess, err := pipeline.OpenSession(filepath.Join(mediaDir, video))
	if err != nil {
		return err
	}
	defer sess.Close()
	planner, err := sess.Planner()
	if err != nil {
		return err
	}
	reqs := sess.ChapterRequests(3)
	if len(reqs) == 0 {
		return nil
	}
	const samplesPerBlock = 50 // forces several durable blocks in the demo file
	continuous := filepath.Join(out, "state", "continuous")
	resumed := filepath.Join(out, "state", "resumed")
	if err := os.RemoveAll(filepath.Join(out, "state")); err != nil {
		return fault.New("demo.state", fault.KindIO, err).WithPath(out)
	}
	contHash, err := batchHash(planner, reqs, continuous, samplesPerBlock, 0)
	if err != nil {
		return err
	}
	if err := batchInterrupt(planner, reqs, resumed, samplesPerBlock); err != nil {
		return err
	}
	resHash, err := batchHash(planner, reqs, resumed, samplesPerBlock, 0)
	if err != nil {
		return err
	}
	if contHash != resHash {
		return fault.Errorf("demo.resume", fault.KindVerify,
			"resumed manifest %s differs from continuous %s", resHash, contHash).Fatal()
	}
	m.Summary.ContinuousHash = contHash
	m.Summary.ResumedHash = resHash
	m.Summary.ResumeIdentical = true
	return nil
}

func batchHash(p *pipeline.Planner, reqs []pipeline.Request, dir string, samplesPerBlock, stop int) (string, error) {
	st, err := checkpoint.Open(dir)
	if err != nil {
		return "", err
	}
	defer st.Close()
	res, err := pipeline.RunBatch(p, reqs, st, pipeline.BatchOptions{SamplesPerBlock: samplesPerBlock, StopAfterBlock: stop})
	if err != nil && res == nil {
		return "", err
	}
	return res.ManifestSHA256, nil
}

func batchInterrupt(p *pipeline.Planner, reqs []pipeline.Request, dir string, samplesPerBlock int) error {
	st, err := checkpoint.Open(dir)
	if err != nil {
		return err
	}
	_, runErr := pipeline.RunBatch(p, reqs, st, pipeline.BatchOptions{SamplesPerBlock: samplesPerBlock, StopAfterBlock: 1})
	if cerr := st.Close(); cerr != nil {
		return cerr
	}
	if runErr == nil {
		return fault.Errorf("demo.interrupt", fault.KindCheckpoint, "simulated interruption did not stop the batch").Fatal()
	}
	fmt.Println("simulated interruption: committed block 1, resuming")
	return nil
}

func pickVideoFile(mediaDir string, names []string) string {
	for _, name := range names {
		if strings.Contains(name, "cam_a") {
			return name
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}
