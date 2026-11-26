package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/fixture"
	"mediad/internal/job"
	"mediad/internal/media"
	"mediad/internal/plan"
)

// demoEnv owns the on-disk layout and collaborators for one demo run.
type demoEnv struct {
	flags      demoFlags
	runDir     string
	inputDir   string
	archiveDir string
	dbPath     string
	store      *job.Store
	archive    *job.Archive
	srcDigest  string
	polDigest  string
	policy     job.Policy
}

func newDemoEnv(f demoFlags) (*demoEnv, error) {
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		return nil, err
	}
	// Each invocation gets an isolated run directory so the pause/resume
	// demonstration always starts from a clean state even when the same
	// root is passed repeatedly. Inputs and archive live underneath it.
	runDir, err := os.MkdirTemp(f.root, "run-*")
	if err != nil {
		return nil, err
	}
	d := &demoEnv{
		flags:      f,
		runDir:     runDir,
		inputDir:   filepath.Join(runDir, "input"),
		archiveDir: filepath.Join(runDir, "archive"),
		dbPath:     filepath.Join(runDir, "state.db"),
	}
	if err := os.MkdirAll(d.inputDir, 0o755); err != nil {
		return nil, err
	}
	store, err := job.OpenStore(d.dbPath)
	if err != nil {
		return nil, err
	}
	archive, err := job.OpenArchive(d.archiveDir)
	if err != nil {
		store.Close()
		return nil, err
	}
	d.store = store
	d.archive = archive
	d.policy = job.Policy{
		MaxBitrate:  f.maxBitrate,
		BudgetUnits: f.budgetUnits,
		MinQuality:  [2]int64{20, 100},
	}
	for _, c := range candidates(f.maxBitrate) {
		d.policy.Candidates = append(d.policy.Candidates, job.PolicyCandidate{
			Width: c.Width, Height: c.Height, Bitrate: c.Bitrate,
			QualityN: c.Quality.Num, QualityD: c.Quality.Den, CostPerNs: c.CostPerNs,
		})
	}
	d.polDigest, err = job.PolicyDigest(d.policy)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// writeFixtures materializes one MP4 track and one RTP track, four samples
// each, plus the EDL containing one clip and one cross-track transition.
func (d *demoEnv) writeFixtures() (mp4Path, rtpPath, edlPath string, err error) {
	mp4Path = filepath.Join(d.inputDir, "video.mp4")
	rtpPath = filepath.Join(d.inputDir, "audio.rtp")
	edlPath = filepath.Join(d.inputDir, "timeline.edl")

	mp4Payloads := make([][]byte, 4)
	durs := make([]uint32, 4)
	for i := 0; i < 4; i++ {
		mp4Payloads[i] = payloadBytes(d.flags.seed, "v", i, 48)
		durs[i] = 1000 // 1 s per sample at a 1000 Hz timescale
	}
	if err := fixture.WriteMP4(fixture.MP4Spec{
		Path: mp4Path, Timescale: 1000,
		SampleDurs: durs, Payload: mp4Payloads,
	}); err != nil {
		return "", "", "", err
	}

	rtpPayloads := make([][]byte, 4)
	for i := 0; i < 4; i++ {
		rtpPayloads[i] = payloadBytes(d.flags.seed, "a", i, 32)
	}
	if err := fixture.WriteRTP(fixture.RTPSpec{
		Path: rtpPath, ClockRate: 1000, // 1 tick = 1 ms
		Timestamps: []uint32{0, 1000, 2000, 3000},
		Payloads:   rtpPayloads, SSRC: uint32(d.flags.seed),
	}); err != nil {
		return "", "", "", err
	}

	// One clip and one cross-track transition, both inside the 4 s span.
	tl := media.NewTimeline([]*media.Track{
		{Key: "video"}, {Key: "audio"},
	})
	tl.Clips = []media.Clip{{TrackKey: "video",
		Interval: media.Interval{StartNs: 0, EndNs: media.NanoPerSecond}}}
	tl.Transitions = []media.Transition{{FromTrack: "video", ToTrack: "audio",
		Interval: media.Interval{StartNs: media.NanoPerSecond, EndNs: 2 * media.NanoPerSecond}}}
	if err := os.WriteFile(edlPath, media.EDLText(tl), 0o644); err != nil {
		return "", "", "", err
	}
	return mp4Path, rtpPath, edlPath, nil
}

// ingest parses both tracks, applies the EDL, and computes the source digest.
func (d *demoEnv) ingest() (*media.Timeline, error) {
	mp4Path, rtpPath, edlPath, err := d.writeFixtures()
	if err != nil {
		return nil, err
	}
	video, err := media.ParseMP4(mp4Path, "video")
	if err != nil {
		return nil, err
	}
	audio, err := media.ParseRTP(rtpPath, "audio")
	if err != nil {
		return nil, err
	}
	tl := media.NewTimeline([]*media.Track{video, audio})
	if err := media.ParseEDLFile(edlPath, tl); err != nil {
		return nil, err
	}
	d.srcDigest = job.SourceDigest(map[string][][]byte{
		"video": samplePayloads(video),
		"audio": samplePayloads(audio),
	})
	return tl, nil
}

func samplePayloads(tr *media.Track) [][]byte {
	out := make([][]byte, 0, len(tr.Samples))
	for _, s := range tr.Samples {
		if s.Payload != nil {
			out = append(out, s.Payload)
		} else {
			out = append(out, []byte(fmt.Sprintf("%s:%d:%d:%d", tr.Key, s.Index, s.StartNs, s.Size)))
		}
	}
	return out
}

// derive runs the planner and returns the chosen configuration.
func (d *demoEnv) derive(tl *media.Timeline) (*plan.Decision, error) {
	dur, err := tl.DurationNs()
	if err != nil {
		return nil, err
	}
	return plan.Derive(candidates(d.flags.maxBitrate), plan.Constraints{
		MinQuality:  plan.Rational{Num: d.policy.MinQuality[0], Den: d.policy.MinQuality[1]},
		MaxBitrate:  d.flags.maxBitrate,
		BudgetUnits: d.flags.budgetUnits,
	}, dur)
}

func (d *demoEnv) runner(tl *media.Timeline, pause int64) (*job.Runner, error) {
	return job.NewRunner(job.Config{
		Store: d.store, Archive: d.archive, Timeline: tl,
		SourceDigest: d.srcDigest, PolicyDigest: d.polDigest,
		PauseAfterSample: pause,
	})
}

func (d *demoEnv) runPaused(tl *media.Timeline) (*job.RunResult, error) {
	r, err := d.runner(tl, 4)
	if err != nil {
		return nil, err
	}
	return r.Run()
}

func (d *demoEnv) runResume(tl *media.Timeline) (*job.RunResult, error) {
	r, err := d.runner(tl, 0)
	if err != nil {
		return nil, err
	}
	return r.Run()
}
