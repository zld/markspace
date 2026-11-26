package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"replay/internal/cmaf"
	"replay/internal/events"
	"replay/internal/manifest"
	"replay/internal/model"
	"replay/internal/planner"
	"replay/internal/verify"
	"replay/internal/worker"
)

// eventTolerance is how far an event may sit from covered media and still
// be accepted (with the window clamped).
const eventTolerance = 500 * time.Millisecond

type setInfo struct {
	name        string
	dir         string
	deliverable bool // failure of a deliverable set fails the run
}

// discoverSets finds media sets: either input itself is one set, or each
// subdirectory containing init.mp4 is. The set named "normal" (or a
// single set) is the deliverable; others are diagnostic.
func discoverSets(input string) ([]setInfo, error) {
	if _, err := os.Stat(join(input, "init.mp4")); err == nil {
		return []setInfo{{name: filepath.Base(filepath.Clean(input)), dir: input, deliverable: true}}, nil
	}
	entries, err := os.ReadDir(input)
	if err != nil {
		return nil, err
	}
	var sets []setInfo
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "out" {
			continue
		}
		dir := join(input, e.Name())
		if _, err := os.Stat(join(dir, "init.mp4")); err == nil {
			sets = append(sets, setInfo{name: e.Name(), dir: dir, deliverable: e.Name() == "normal"})
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].name < sets[j].name })
	if len(sets) == 0 {
		return nil, fmt.Errorf("no media sets found under %s", input)
	}
	return sets, nil
}

func runPipeline(o options) int {
	sets, err := discoverSets(o.input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		return 1
	}
	eventsPath := resolveEventsPath(o.input)
	evs, err := events.Load(eventsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo: events:", err)
		return 1
	}
	ev := events.Find(evs, o.event)
	if ev == nil {
		fmt.Fprintf(os.Stderr, "demo: event %q not found in %s\n", o.event, eventsPath)
		return 2
	}
	exit := 0
	var deliverableManifest string
	for _, s := range sets {
		mpath, ok := processSet(s, o, ev)
		if s.deliverable {
			if !ok {
				exit = 1
			} else {
				deliverableManifest = mpath
			}
		}
	}
	if o.verify {
		if deliverableManifest == "" {
			fmt.Fprintln(os.Stderr, "demo: verify: no deliverable manifest produced")
			return 1
		}
		return verifyDeliverable(deliverableManifest, sets, o, eventsPath)
	}
	return exit
}

// resolveEventsPath locates events.json: first inside input (fixture root),
// then one directory up so pointing --input directly at a set directory
// still finds the fixture-level event records. It returns the conventional
// in-input path when neither exists so the loader reports a clear error.
func resolveEventsPath(input string) string {
	direct := join(input, "events.json")
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	parent := join(input, "..", "events.json")
	if _, err := os.Stat(parent); err == nil {
		return parent
	}
	return direct
}

// processSet runs the slice pipeline for one set. It returns the manifest
// path and whether the set was processed successfully.
func processSet(s setInfo, o options, ev *events.Event) (string, bool) {
	fmt.Printf("== set %s ==\n", s.name)
	rep := &model.Report{}
	p := cmaf.NewParser(rep)
	init, err := p.ParseInit(join(s.dir, "init.mp4"))
	if err != nil {
		fmt.Println("  fatal: parse init:", err)
		return "", false
	}
	var frags []*cmaf.Fragment
	for _, fp := range fragmentFiles(s.dir) {
		f, err := p.ParseFragment(fp, init)
		if err != nil {
			fmt.Println("  fatal: parse:", err)
			return "", false
		}
		frags = append(frags, f)
	}
	tl := cmaf.BuildTimeline(frags, init, rep)
	printTimeline(tl, rep)
	if rep.HasFatal() {
		return "", false
	}

	win, err := events.WindowFor(*ev, o.before, o.after, tl, eventTolerance, rep)
	if err != nil {
		fmt.Println("  rejected:", err)
		return "", false
	}
	cand, reasons := planner.Evaluate(planner.Request{
		Window:        win,
		TargetBitrate: o.bitrate,
		AudioIn:       audioInfoOf(init),
		ColorIn:       colorInfoOf(init),
	}, planner.DefaultLimits(), rep)
	if reasons != nil {
		for _, r := range reasons {
			fmt.Println("  rejected:", r)
		}
		return "", false
	}
	printPlan(cand)

	outDir := join(o.input, "out", s.name)
	job := buildJob(s, o, tl, win, cand, outDir)
	results, err := worker.New(o.workers).Run(context.Background(), []worker.Job{job})
	if err != nil {
		fmt.Println("  worker:", err)
		return "", false
	}
	res := results[0]
	if res.Reused {
		fmt.Println("  worker: reused previous result (idempotent re-run)")
	}

	m := buildManifest(s, o, win, tl, cand, res, rep, len(frags))
	mpath := join(outDir, o.event+".manifest.json")
	if err := manifest.Write(mpath, m); err != nil {
		fmt.Println("  manifest:", err)
		return "", false
	}
	fmt.Printf("  output: %s (%d bytes)\n", res.OutputPath, res.SizeBytes)
	fmt.Printf("  output sha256: %s\n", res.SHA256)
	fmt.Printf("  manifest: %s\n  manifest hash: %s\n", mpath, m.Hash)
	return mpath, true
}

func printTimeline(tl *cmaf.Timeline, rep *model.Report) {
	fmt.Printf("  timeline: segments=%d end=%v gaps=%d dups=%d tfdt_regressions=%d missing=%d\n",
		len(tl.Segments), tl.End.Round(time.Millisecond), tl.SeqGaps, tl.Duplicates,
		tl.Regressions, len(tl.Missing))
	for _, m := range tl.Missing {
		fmt.Printf("  missing media: [%v, %v)\n", m.Start.Round(time.Millisecond), m.End.Round(time.Millisecond))
	}
	for _, iss := range rep.Issues {
		fmt.Println("  issue:", iss)
	}
}

func printPlan(c *planner.Candidate) {
	fmt.Printf("  plan: duration=%v video_bitrate=%d audio_bitrate=%d est_bytes=%d cost=%d\n",
		c.Duration.Round(time.Millisecond), c.VideoBitrate, c.AudioBitrate, c.EstBytes, c.CostUnits)
	fmt.Printf("  audio: %s %dHz (resample=%t)\n", c.Audio.Layout, c.Audio.SampleRate, c.Audio.Resample)
	fmt.Printf("  color: %s (convert=%t)\n", c.Color.Note, c.Color.Convert)
}

func verifyDeliverable(mpath string, sets []setInfo, o options, eventsPath string) int {
	var s setInfo
	for _, x := range sets {
		if x.deliverable {
			s = x
		}
	}
	out, err := verify.Verify(mpath, s.dir, eventsPath, verify.DefaultConstraints())
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo: verify:", err)
		return 1
	}
	fmt.Println("== verify ==")
	for _, c := range out.Checks {
		status := "OK"
		if !c.OK {
			status = "FAIL"
		}
		fmt.Printf("  %-14s %-4s %s\n", c.Name, status, c.Detail)
	}
	if !out.OK {
		fmt.Println("VERIFY FAILED")
		return 1
	}
	fmt.Println("VERIFY OK")
	return 0
}

// fragmentFiles lists *.m4s in dir, sorted by name (file order).
func fragmentFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".m4s") {
			out = append(out, join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func audioInfoOf(init *cmaf.Init) model.AudioInfo {
	if a := init.Audio(); a != nil && a.Audio != nil {
		return *a.Audio
	}
	return model.AudioInfo{}
}

func colorInfoOf(init *cmaf.Init) *model.ColorInfo {
	if v := init.Video(); v != nil {
		return v.Color
	}
	return nil
}

// fileSHA256 hashes a file's contents.
func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// secStr formats a duration as seconds with millisecond precision.
func secStr(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
