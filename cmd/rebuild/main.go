// Command rebuild reconstructs one review timeline from client events, RTP
// media captures and subtitles, renders offscreen review frames, and publishes
// everything through a checksummed journal with crash recovery.
//
// Usage:
//
//	rebuild [-selftest] [-recover] [-verify] [-rollback] -out DIR
//	        [-rate 24|25|29.97|50] [-frames N] [-crash-after POINT]
//
// Exit status: 0 processed/recovered, 2 invalid input, 75 injected crash.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"media.rebuild/internal/clock"
	"media.rebuild/internal/fixtures"
	"media.rebuild/internal/journal"
	"media.rebuild/internal/manifest"
	"media.rebuild/internal/model"
	"media.rebuild/internal/mp4"
	"media.rebuild/internal/render"
	"media.rebuild/internal/rtp"
	"media.rebuild/internal/subtitle"
	"media.rebuild/internal/timeline"
	"media.rebuild/internal/txn"
)

// Exit statuses.
const (
	exitOK    = 0
	exitInput = 2
	exitCrash = 75
)

// taskName is the single transactional task published by this tool.
const taskName = "review"

const (
	subtitleTrackName = "captions"
	mediaTrackName    = "media"
	clientTrackName   = "client"
	// mediaToleranceNs is how far a media sample may sit from a client event
	// and still be considered the same instant.
	mediaToleranceNs = 20_000_000
)

// options are the parsed command line.
type options struct {
	selftest   bool
	recover    bool
	verify     bool
	rollback   bool
	out        string
	crashAfter string
	rate       string
	frames     int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "argument error: %v\n", err)
		return exitInput
	}
	if opts.out == "" {
		fmt.Fprintln(stderr, "argument error: -out is required")
		return exitInput
	}
	if clean := filepath.Clean(opts.out); clean == "." || clean == "/" {
		fmt.Fprintf(stderr, "argument error: refusing output root %q\n", opts.out)
		return exitInput
	}
	switch {
	case opts.recover:
		return doRecover(opts, stdout, stderr)
	case opts.rollback:
		return doRollback(opts, stdout, stderr)
	case opts.verify:
		return doVerify(opts, stdout, stderr)
	case opts.selftest:
		return doSelfTest(opts, stdout, stderr)
	default:
		return doRun(opts, stdout, stderr)
	}
}

func parseArgs(args []string) (options, error) {
	var o options
	o.rate = "25"
	o.frames = 8
	crashPoints := map[string]bool{
		string(txn.CrashAfterArtifact):  true,
		string(txn.CrashAfterStageDone): true,
		string(txn.CrashAfterManifest):  true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch {
		case a == "-selftest":
			o.selftest = true
		case a == "-recover":
			o.recover = true
		case a == "-verify":
			o.verify = true
		case a == "-rollback":
			o.rollback = true
		case a == "-out":
			o.out, err = next()
			if err != nil {
				return o, err
			}
		case a == "-rate":
			o.rate, err = next()
			if err != nil {
				return o, err
			}
		case a == "-frames":
			var v string
			v, err = next()
			if err != nil {
				return o, err
			}
			o.frames, err = strconv.Atoi(v)
			if err != nil || o.frames <= 0 || o.frames > 1000 {
				return o, fmt.Errorf("-frames must be 1..1000, got %q", v)
			}
		case a == "-crash-after":
			o.crashAfter, err = next()
			if err != nil {
				return o, err
			}
			if !crashPoints[o.crashAfter] {
				return o, fmt.Errorf("unknown crash point %q", o.crashAfter)
			}
		case a == "-crash-after-artifact":
			// The acceptance script's shorthand for the first crash point.
			o.crashAfter = string(txn.CrashAfterArtifact)
		case a == "-h", a == "-help", a == "--help":
			return o, fmt.Errorf("usage: rebuild [-selftest] [-recover] [-verify] [-rollback] -out DIR [-rate R] [-frames N] [-crash-after POINT]")
		case strings.HasPrefix(a, "-crash-after="):
			o.crashAfter = strings.TrimPrefix(a, "-crash-after=")
			if !crashPoints[o.crashAfter] {
				return o, fmt.Errorf("unknown crash point %q", o.crashAfter)
			}
		case strings.HasPrefix(a, "-out="):
			o.out = strings.TrimPrefix(a, "-out=")
		case strings.HasPrefix(a, "-rate="):
			o.rate = strings.TrimPrefix(a, "-rate=")
		case strings.HasPrefix(a, "-frames="):
			n, cerr := strconv.Atoi(strings.TrimPrefix(a, "-frames="))
			if cerr != nil || n <= 0 || n > 1000 {
				return o, fmt.Errorf("bad -frames %q", a)
			}
			o.frames = n
		default:
			return o, fmt.Errorf("unknown argument %q", a)
		}
	}
	return o, nil
}

// sandbox keeps every generated input inside the output directory.
type sandbox struct {
	root string
}

func (s sandbox) inputDir() string { return filepath.Join(s.root, "input") }

// write stores bytes under the input directory, refusing names that escape it.
func (s sandbox) write(name string, data []byte) (string, error) {
	if strings.ContainsAny(name, `/\`) || name == "" || name == "." || name == ".." {
		return "", model.Newf(model.CodeInvalidArgument, model.Source{Name: name, Offset: -1},
			"input name %q must be a plain file name", name)
	}
	dir := s.inputDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", model.New(model.CodeIO, model.Source{Name: dir, Offset: -1}, "create input directory", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "write input", err)
	}
	return path, nil
}

func (s sandbox) read(name string) ([]byte, string, error) {
	path := filepath.Join(s.inputDir(), name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "read input", err)
	}
	return data, path, nil
}

// runState is the outcome of processing one output root.
type runState struct {
	summary *manifest.Summary
	frames  []render.Frame
	crashed bool
	reused  bool
}

// build runs the whole pipeline and publishes through txn.
func build(opts options, stdout, stderr io.Writer, crash txn.CrashPoint) (*runState, error) {
	rate, err := clock.ParseRate(opts.rate)
	if err != nil {
		return nil, err
	}
	sb := sandbox{root: opts.out}
	eng, err := txn.OpenRoot(opts.out, crash)
	if err != nil {
		return nil, err
	}
	defer eng.Close()

	if err := generateInputs(sb); err != nil {
		return nil, err
	}
	summary, frames, err := analyze(sb, rate, opts.frames)
	if err != nil {
		return nil, err
	}

	// A root with a committed manifest is owned by recovery; a rerun reports
	// the durable summary and checks reproducibility without touching the
	// artifact set.
	if m, err := txn.ReadManifest(opts.out); err == nil && m.Committed {
		if m.Summary != nil {
			fmt.Fprintf(stdout, "rerun reused=true frame_sha256_match=%t committed_artifacts=%d\n",
				m.Summary.FrameDigest == summary.FrameDigest, len(m.Artifacts))
			return &runState{summary: m.Summary, reused: true}, nil
		}
	}

	if err := eng.Begin(taskName, "render"); err != nil {
		return nil, err
	}
	var arts []manifest.Artifact
	for _, f := range frames {
		a, err := eng.Stage(taskName, f.File, f.Data)
		if err != nil {
			return nil, err
		}
		arts = append(arts, a)
	}
	// The summary artifact carries the full acceptance record.
	summary.FrameList = make([]manifest.FrameRecord, 0, len(frames))
	for _, f := range frames {
		summary.FrameList = append(summary.FrameList, manifest.FrameRecord{
			Index: f.Index, TimeNs: f.TimeNs, SourceEvent: f.SourceEvent,
			File: f.File, Bytes: f.Bytes, SHA256: f.SHA256,
		})
	}
	summary.Frames = len(frames)
	summary.FrameDigest = render.Digest(frames)
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: opts.out, Offset: -1}, "encode summary", err)
	}
	data = append(data, '\n')
	sa, err := eng.Stage(taskName, "summary.json", data)
	if err != nil {
		return nil, err
	}
	arts = append(arts, sa)

	m := manifest.Manifest{Task: taskName, Generation: 1, Artifacts: arts, Summary: summary}
	if err := eng.Seal(m); err != nil {
		return crashResult(err, crash, summary, stderr)
	}
	if err := eng.Publish(taskName); err != nil {
		return crashResult(err, crash, summary, stderr)
	}
	if err := eng.Commit(m); err != nil {
		return crashResult(err, crash, summary, stderr)
	}
	return &runState{summary: summary, frames: frames}, nil
}

// crashResult maps an injected-crash error to a state the CLI can exit on.
// A crash message names the phase that was reached and that nothing committed.
func crashResult(err error, crash txn.CrashPoint, summary *manifest.Summary, stderr io.Writer) (*runState, error) {
	if isCrash(err) {
		fmt.Fprintf(stderr, "phase=%s committed=false\n", txn.PhaseOf(crash))
		return &runState{summary: summary, crashed: true}, err
	}
	return nil, err
}

func isCrash(err error) bool {
	return err != nil && strings.Contains(err.Error(), txn.ErrCrashInjected.Error())
}

// generateInputs writes every fixture under the sandbox. Files are regenerated
// on every run from constants, so runs are comparable byte for byte.
func generateInputs(sb sandbox) error {
	if _, err := sb.write("events.csv", []byte(fixtures.EventsText())); err != nil {
		return err
	}
	if _, err := sb.write("clip.mp4", fixtures.DefaultMP4().Bytes()); err != nil {
		return err
	}
	// A second clip whose final sample lies outside the file: exactly one
	// sample is rejected and the damaged track yields no samples at all.
	bad := fixtures.DefaultMP4()
	bad.SampleCount = 3
	bad.OversizeSample = 2
	if _, err := sb.write("clip-truncated.mp4", bad.Bytes()); err != nil {
		return err
	}
	playback, err := fixtures.Capture(fixtures.PlaybackPackets())
	if err != nil {
		return err
	}
	if _, err := sb.write("playback.rtpj", playback); err != nil {
		return err
	}
	// The truncated capture is a separate healthy stream that was cut mid
	// datagram, so its truncation is the only anomaly it carries.
	healthy, err := fixtures.Capture(fixtures.MediaPackets(5))
	if err != nil {
		return err
	}
	if _, err := sb.write("torn.rtpj", fixtures.TruncateLast(healthy, 7)); err != nil {
		return err
	}
	for _, f := range []struct{ name, text string }{
		{"broken.srt", fixtures.BrokenSRT()},
		{"broken.vtt", fixtures.BrokenVTT()},
		{"review.srt", fixtures.ReviewSRT()},
		{"review.vtt", fixtures.ReviewVTT()},
	} {
		if _, err := sb.write(f.name, []byte(f.text)); err != nil {
			return err
		}
	}
	var tc bytes.Buffer
	for _, line := range fixtures.TimecodeSheet() {
		tc.WriteString(line)
		tc.WriteByte('\n')
	}
	if _, err := sb.write("timecodes.txt", tc.Bytes()); err != nil {
		return err
	}
	return nil
}

func inputNames(root string) []string {
	entries, _ := os.ReadDir(filepath.Join(root, "input"))
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// analyze runs every subsystem and assembles the acceptance summary.
func analyze(sb sandbox, rate clock.Rate, frameCount int) (*manifest.Summary, []render.Frame, error) {
	summary := &manifest.Summary{ThresholdMs: timeline.ThresholdNs / 1_000_000}
	summary.Anomalies = []manifest.Anomaly{}
	var errs []string
	addErr := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	// --- client events ---------------------------------------------------
	events, eventErrs := parseEvents(filepath.Join(sb.inputDir(), "events.csv"))
	for _, e := range eventErrs {
		addErr(e)
	}

	// --- stage journal ---------------------------------------------------
	// Every analysis stage records a durable begin/done pair. The entries are
	// appended to the stage log inside the output root, separate from the
	// transaction journal, so a reviewer can see which stages completed.
	stageLog, err := journal.Open(filepath.Join(sb.root, "journal", "stages.wal"))
	if err != nil {
		return nil, nil, err
	}
	defer stageLog.Close()
	runStage := func(name string, fn func()) {
		if _, err := stageLog.Append(journal.Record{Type: journal.TypeStageBegin, Task: taskName, Stage: name}); err != nil {
			addErr(err)
		}
		fn()
		if _, err := stageLog.Append(journal.Record{Type: journal.TypeStageDone, Task: taskName, Stage: name}); err != nil {
			addErr(err)
		}
	}
	for _, e := range events {
		// Each event is also labelled in the review frame rate's timecode, so
		// the rate selection is visible in the published record. Drop-frame
		// labels are used whenever the rate defines them.
		detail := e.Label
		if frame, ferr := clock.FrameAt(rate, e.TimeNs); ferr == nil {
			if tc, terr := clock.FormatFrame(rate, frame, rate.SupportsDropFrame()); terr == nil {
				detail = fmt.Sprintf("%s frame=%d tc=%s", e.Label, frame, tc)
			}
		}
		summary.Items = append(summary.Items, manifest.ItemConclusion{
			ID: e.ID, Kind: "event", Track: clientTrackName, Source: e.Source, Line: e.Line,
			TimeNs: e.TimeNs, Status: "ok", Detail: detail,
		})
	}

	// --- subtitles -------------------------------------------------------
	var cues []timeline.CueRef
	runStage("subtitles", func() {
		for _, name := range []string{"broken.srt", "broken.vtt", "review.srt", "review.vtt"} {
			data, path, err := sb.read(name)
			if err != nil {
				addErr(err)
				continue
			}
			kind := subtitle.Detect(string(data))
			tr, perr := subtitle.Parse(kind, name, string(data))
			if perr != nil {
				addErr(perr)
				continue
			}
			_ = path
			for _, e := range tr.Errors {
				addErr(e)
				summary.Items = append(summary.Items, manifest.ItemConclusion{
					ID: fmt.Sprintf("%s:%d", name, e.Source.Line), Kind: "subtitle", Track: subtitleTrackName,
					Source: name, Line: e.Source.Line, Status: "error", Detail: e.Error(),
				})
			}
			for _, c := range tr.Cues {
				id := fmt.Sprintf("%s:%d", name, c.Line)
				cues = append(cues, timeline.CueRef{
					ID: id, Track: subtitleTrackName, Source: name, Line: c.Line,
					TimeNs: c.Start, EndNs: c.End, Label: c.Text,
				})
				summary.Items = append(summary.Items, manifest.ItemConclusion{
					ID: id, Kind: "subtitle", Track: subtitleTrackName, Source: name, Line: c.Line,
					TimeNs: c.Start, Status: "ok", Detail: fmt.Sprintf("[%d,%d)ns", c.Start, c.End),
				})
			}
			// Overlapping cues cannot both be on screen at the same instant;
			// report the intersection so the review accounts for it.
			byID := make(map[string]subtitle.Cue, len(tr.Cues))
			for _, c := range tr.Cues {
				byID[fmt.Sprintf("%s:%d", name, c.Line)] = c
			}
			for _, pair := range subtitle.Overlap(tr.Cues) {
				a := tr.Cues[pair[0]]
				b := tr.Cues[pair[1]]
				start := a.Start
				if b.Start > start {
					start = b.Start
				}
				end := a.End
				if b.End < end {
					end = b.End
				}
				summary.Items = append(summary.Items, manifest.ItemConclusion{
					ID: fmt.Sprintf("%s:overlap:%d-%d", name, a.Line, b.Line), Kind: "subtitle",
					Track: subtitleTrackName, Source: name, Line: a.Line, TimeNs: start,
					Status: "ok", Detail: fmt.Sprintf("cues overlap over [%d,%d)ns", start, end),
				})
			}
		}
	})

	// --- RTP -------------------------------------------------------------
	var mediaSamples []timeline.SampleRef
	runStage("rtp", func() {
		for _, name := range []string{"playback.rtpj", "torn.rtpj"} {
			data, path, err := sb.read(name)
			if err != nil {
				addErr(err)
				continue
			}
			// Report the capture by name, not by absolute path, so a manifest
			// does not depend on where the output root lives.
			_ = path
			cap, err := rtp.Read(bytes.NewReader(data), name, rtp.Options{})
			if err != nil {
				addErr(err)
				continue
			}
			dup, gaps, missing := cap.Totals()
			summary.RTPDuplicate += dup
			summary.RTPGap += gaps
			summary.RTPMissing += missing
			for _, e := range cap.Errors {
				addErr(e)
				summary.Items = append(summary.Items, manifest.ItemConclusion{
					ID: fmt.Sprintf("%s@%d", name, e.Source.Offset), Kind: "rtp", Track: mediaTrackName,
					Source: name, Status: "error", Detail: e.Error(),
				})
			}
			for _, rec := range cap.Records {
				if rec.Duplicate {
					continue
				}
				id := fmt.Sprintf("%s:seq%d", name, rec.ExtSequence)
				mediaSamples = append(mediaSamples, timeline.SampleRef{
					ID: id, Track: mediaTrackName, Source: name, TimeNs: rec.TimeNs,
					Label: fmt.Sprintf("ssrc=%d seq=%d", rec.SSRC, rec.ExtSequence),
				})
				summary.Items = append(summary.Items, manifest.ItemConclusion{
					ID: id, Kind: "rtp", Track: mediaTrackName, Source: name,
					TimeNs: rec.TimeNs, Status: "ok", Detail: fmt.Sprintf("payload=%d", rec.PayloadLen),
				})
			}
		}
	})

	// --- MP4 -------------------------------------------------------------
	var mp4Invalid int
	runStage("mp4", func() {
		for _, name := range []string{"clip-truncated.mp4", "clip.mp4"} {
			idx, err := indexMP4(filepath.Join(sb.inputDir(), name), name)
			if err != nil {
				addErr(err)
				continue
			}
			mp4Invalid += idx.InvalidSamples()
			for _, e := range idx.Errors {
				addErr(e)
			}
			for _, tr := range idx.Tracks {
				if !tr.Valid {
					// One conclusion per damaged track, with every rejected
					// sample listed in its detail, so the item list does not grow
					// with the damage count.
					details := make([]string, 0, len(tr.Errors))
					for _, e := range tr.Errors {
						addErr(e)
						details = append(details, e.Error())
					}
					summary.Items = append(summary.Items, manifest.ItemConclusion{
						ID: fmt.Sprintf("%s:track%d", name, tr.TrackID), Kind: "mp4", Track: mediaTrackName,
						Source: name, Status: "error",
						Detail: fmt.Sprintf("rejected=%d; %s", len(details), strings.Join(details, "; ")),
					})
					continue
				}
				for _, s := range tr.Samples {
					id := fmt.Sprintf("%s:t%d:s%d", name, tr.TrackID, s.Index)
					mediaSamples = append(mediaSamples, timeline.SampleRef{
						ID: id, Track: mediaTrackName, Source: name, TimeNs: s.TimeNs,
						Label: fmt.Sprintf("sample=%d dts=%d", s.Index, s.DTS),
					})
					summary.Items = append(summary.Items, manifest.ItemConclusion{
						ID: id, Kind: "mp4", Track: mediaTrackName, Source: name,
						TimeNs: s.TimeNs, Status: "ok", Detail: fmt.Sprintf("bytes=%d", s.Size),
					})
				}
			}
		}
	})
	summary.MP4Invalid = mp4Invalid

	// --- timecode validation --------------------------------------------
	// The sheet is 29.97 drop-frame material and is always validated against
	// that rate, independent of the review frame rate: drop-frame rules are
	// only defined for 30000/1001.
	_ = rate
	if data, _, err := sb.read("timecodes.txt"); err == nil {
		for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			line = strings.TrimSpace(line)
			if i == 0 || line == "" {
				continue
			}
			src := model.Source{Name: "timecodes.txt", Line: i + 1, Offset: -1}
			if _, err := clock.ParseTimecodeAt(line, clock.Rate2997, src); err != nil {
				addErr(err)
			}
		}
	}

	// --- timeline --------------------------------------------------------
	var merged *timeline.Result
	runStage("merge", func() {
		merged, err = timeline.Merge(events, mediaSamples, cues, timeline.WithMediaTolerance(mediaToleranceNs))
	})
	if err != nil {
		return nil, nil, err
	}
	summary.MaxSkewNs = merged.MaxSkewNs
	summary.MaxSkewMs = msCeil(merged.MaxSkewNs)
	// Coverage is the span from the earliest to the latest item on any
	// track; it is reported so a reviewer can see what the run examined.
	summary.CoverageStartNs, summary.CoverageEndNs = coverage(merged.Items)
	summary.OverBudget = merged.OverBudget
	summary.Unmatched = merged.Unmatched
	summary.AnomalyRanges = len(merged.Anomalies)
	for _, a := range merged.Anomalies {
		summary.Anomalies = append(summary.Anomalies, manifest.Anomaly{
			GroupIDs: a.GroupIDs, StartNs: a.StartNs, EndNs: a.EndNs, MaxSkewNs: a.MaxSkewNs,
			Tracks: a.Tracks, Items: a.Items,
		})
	}
	// Fold timeline verdicts back into per-item conclusions so every reported
	// item carries the deviation that drove the budget decision.
	byID := make(map[string]timeline.Item, len(merged.Items))
	for _, it := range merged.Items {
		byID[it.ID] = it
	}
	for i := range summary.Items {
		item := &summary.Items[i]
		it, ok := byID[item.ID]
		if !ok {
			continue
		}
		item.SkewNs = it.SkewNs
		item.Over = it.Over
		item.Group = it.Group
	}

	// --- frames ----------------------------------------------------------
	var frames []render.Frame
	runStage("render", func() {
		frames, err = renderFrames(merged, frameCount)
	})
	if err != nil {
		return nil, nil, err
	}
	summary.Frames = len(frames)
	summary.FrameDigest = render.Digest(frames)

	summary.ItemsTotal = len(summary.Items)
	for _, item := range summary.Items {
		if item.Status == "ok" {
			summary.ItemsOK++
		} else {
			summary.ItemsError++
		}
	}
	sort.SliceStable(summary.Items, func(i, j int) bool {
		a, b := summary.Items[i], summary.Items[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.ID < b.ID
	})
	// Inputs are digested last so verification can re-derive them.
	for _, name := range inputNames(sb.root) {
		path := filepath.Join(sb.inputDir(), name)
		data, err := os.ReadFile(path)
		if err != nil {
			addErr(err)
			continue
		}
		sum := sha256.Sum256(data)
		summary.Inputs = append(summary.Inputs, manifest.InputDigest{
			Path: filepath.ToSlash(filepath.Join("input", name)), Kind: kindOf(name),
			Bytes: len(data), SHA256: hex.EncodeToString(sum[:]),
		})
	}
	sort.Strings(errs)
	summary.Errors = errs
	return summary, frames, nil
}

// indexMP4 indexes a clip, attributing errors to displayName rather than the
// absolute path so manifests are location independent.
func indexMP4(path, displayName string) (*mp4.Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: displayName, Offset: -1}, "open mp4", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: displayName, Offset: -1}, "stat mp4", err)
	}
	return mp4.Read(f, displayName, st.Size())
}

func kindOf(name string) string {
	switch {
	case strings.HasSuffix(name, ".mp4"):
		return "mp4"
	case strings.HasSuffix(name, ".rtpj"):
		return "rtp"
	case strings.HasSuffix(name, ".srt"):
		return "srt"
	case strings.HasSuffix(name, ".vtt"):
		return "vtt"
	case strings.HasSuffix(name, ".csv"):
		return "events"
	default:
		return "text"
	}
}

// parseEvents reads "id,client_ms,media_ms,label" lines.
func parseEvents(path string) ([]timeline.Event, []*model.Error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []*model.Error{model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "read events", err)}
	}
	var events []timeline.Event
	var errs []*model.Error
	for i, raw := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		src := model.Source{Name: filepath.Base(path), Line: i + 1, Offset: -1}
		if len(fields) != 4 {
			errs = append(errs, model.Newf(model.CodeInvalidArgument, src, "event line needs 4 fields, got %d", len(fields)))
			continue
		}
		clientMS, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		if err != nil || clientMS < 0 {
			errs = append(errs, model.Newf(model.CodeInvalidArgument, src, "bad client time %q", fields[1]))
			continue
		}
		var mediaMS int64
		hasMedia := strings.TrimSpace(fields[2]) != ""
		if hasMedia {
			mediaMS, err = strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
			if err != nil || mediaMS < 0 {
				errs = append(errs, model.Newf(model.CodeInvalidArgument, src, "bad media time %q", fields[2]))
				continue
			}
		}
		ev := timeline.Event{
			ID: strings.TrimSpace(fields[0]), Track: clientTrackName, Source: filepath.Base(path),
			Line: i + 1, TimeNs: clientMS * 1_000_000, Label: strings.TrimSpace(fields[3]),
		}
		if hasMedia {
			ev.MediaTimeNs = mediaMS * 1_000_000
		}
		events = append(events, ev)
	}
	return events, errs
}

// renderFrames samples the merged timeline at the client event instants: the
// places where a playback fault is observed. The schedule is fixed by the
// event log, so it does not move with media alignment.
func renderFrames(merged *timeline.Result, limit int) ([]render.Frame, error) {
	var anchors []timeline.Item
	for _, it := range merged.Items {
		if it.Kind == timeline.KindEvent {
			anchors = append(anchors, it)
		}
	}
	sort.SliceStable(anchors, func(i, j int) bool {
		if anchors[i].TimeNs != anchors[j].TimeNs {
			return anchors[i].TimeNs < anchors[j].TimeNs
		}
		return anchors[i].ID < anchors[j].ID
	})
	if len(anchors) == 0 {
		return nil, nil
	}
	if limit > 0 && len(anchors) > limit {
		anchors = anchors[:limit]
	}
	spanStart := anchors[0].TimeNs
	spanEnd := anchors[len(anchors)-1].TimeNs
	if spanEnd == spanStart {
		spanEnd = spanStart + 1_000_000_000
	}
	var frames []render.Frame
	for i, a := range anchors {
		spec := render.Spec{
			Index: i, TimeNs: a.TimeNs, SpanStartNs: spanStart, SpanEndNs: spanEnd,
			SourceEvent: a.ID,
		}
		lanes := map[string]*render.Lane{}
		var subtitles []string
		for _, it := range merged.Items {
			switch {
			case it.Kind == timeline.KindEvent:
				if it.ID != a.ID {
					continue
				}
				l := lane(lanes, it.Track, "event")
				l.Active = true
				l.Items++
				l.Over = l.Over || it.Over
				l.SkewNs = maxInt64(l.SkewNs, it.AbsSkewNs)
			case it.Kind == timeline.KindMedia && it.Group == a.Group:
				l := lane(lanes, it.Track, "media")
				l.Active = true
				l.Items++
				l.Over = l.Over || it.Over
				l.SkewNs = maxInt64(l.SkewNs, it.AbsSkewNs)
			case it.Kind == timeline.KindSubtitle && it.TimeNs <= a.TimeNs && (it.EndNs == 0 || it.EndNs > a.TimeNs):
				l := lane(lanes, it.Track, "subtitle")
				l.Active = true
				l.Items++
				subtitles = append(subtitles, it.Label)
			}
		}
		sort.Strings(subtitles)
		spec.Subtitle = strings.Join(subtitles, " | ")
		names := make([]string, 0, len(lanes))
		for n := range lanes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			spec.Lanes = append(spec.Lanes, *lanes[n])
		}
		f, err := render.RenderFrame(spec, fmt.Sprintf("frame-%03d.png", i))
		if err != nil {
			return nil, err
		}
		frames = append(frames, *f)
	}
	return frames, nil
}

// msCeil converts nanoseconds to milliseconds, rounding up so a deviation just
// over the budget never prints as the budget itself.
func msCeil(ns int64) int64 {
	if ns <= 0 {
		return 0
	}
	return (ns + 999_999) / 1_000_000
}

// coverage returns the earliest instant and the latest end across items.
func coverage(items []timeline.Item) (int64, int64) {
	if len(items) == 0 {
		return 0, 0
	}
	start := items[0].TimeNs
	var end int64
	for _, it := range items {
		if it.TimeNs < start {
			start = it.TimeNs
		}
		e := it.TimeNs
		if it.EndNs > e {
			e = it.EndNs
		}
		if e > end {
			end = e
		}
	}
	return start, end
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func lane(m map[string]*render.Lane, track, kind string) *render.Lane {
	l, ok := m[track]
	if !ok {
		l = &render.Lane{Track: track, Kind: kind}
		m[track] = l
	}
	return l
}

// printSummary writes the human-readable acceptance record.
func printSummary(w io.Writer, s *manifest.Summary, mode string) {
	fmt.Fprintf(w, "mode=%s\n", mode)
	fmt.Fprintf(w, "items_total=%d items_ok=%d items_error=%d\n", s.ItemsTotal, s.ItemsOK, s.ItemsError)
	fmt.Fprintf(w, "max_skew_ms=%d threshold_ms=%d over_budget=%d anomaly_ranges=%d items_unmatched=%d\n",
		s.MaxSkewMs, s.ThresholdMs, s.OverBudget, s.AnomalyRanges, s.Unmatched)
	fmt.Fprintf(w, "coverage=[%d,%d)ns\n", s.CoverageStartNs, s.CoverageEndNs)
	fmt.Fprintf(w, "frames=%d frame_sha256=%s\n", s.Frames, s.FrameDigest)
	fmt.Fprintf(w, "rtp_duplicate=%d rtp_gap=%d rtp_missing=%d mp4_invalid_samples=%d\n",
		s.RTPDuplicate, s.RTPGap, s.RTPMissing, s.MP4Invalid)
	for _, a := range s.Anomalies {
		fmt.Fprintf(w, "anomaly groups=%s span=[%d,%d)ns max_skew_ms=%d tracks=%s items=%s\n",
			strings.Join(a.GroupIDs, ","), a.StartNs, a.EndNs, a.MaxSkewNs/1_000_000,
			strings.Join(a.Tracks, ","), strings.Join(a.Items, ","))
	}
	fmt.Fprintln(w, "items:")
	for _, it := range s.Items {
		fmt.Fprintf(w, "  %-8s %-32s status=%-5s skew_ms=%d over=%t group=%s",
			it.Kind, it.ID, it.Status, it.SkewNs/1_000_000, it.Over, it.Group)
		if it.Detail != "" {
			fmt.Fprintf(w, " detail=%q", it.Detail)
		}
		fmt.Fprintln(w)
	}
	if len(s.Errors) > 0 {
		fmt.Fprintf(w, "input_errors=%d\n", len(s.Errors))
	}
}

func doSelfTest(opts options, stdout, stderr io.Writer) int {
	crash := txn.CrashPoint(opts.crashAfter)
	st, err := build(opts, stdout, stderr, crash)
	if err != nil {
		if isCrash(err) {
			return exitCrash
		}
		fmt.Fprintf(stderr, "selftest failed: %v\n", err)
		return exitInput
	}
	if st.crashed {
		return exitCrash
	}
	printSummary(stdout, st.summary, "selftest")
	if st.summary == nil || st.summary.ItemsTotal == 0 || st.summary.Frames == 0 {
		fmt.Fprintln(stderr, "selftest produced no items or frames")
		return exitInput
	}
	if !st.reused {
		if code := reportRerunCheck(opts, st, stdout, stderr); code != exitOK {
			return code
		}
	}
	return exitOK
}

func doRun(opts options, stdout, stderr io.Writer) int {
	st, err := build(opts, stdout, stderr, txn.CrashPoint(opts.crashAfter))
	if err != nil {
		if isCrash(err) {
			return exitCrash
		}
		fmt.Fprintf(stderr, "run failed: %v\n", err)
		return exitInput
	}
	printSummary(stdout, st.summary, "run")
	return exitOK
}

// reportRerunCheck re-derives the frames from the same generated inputs and
// compares them with what the committed manifest records. It also counts the
// published artifact set so a caller can see the rerun changed nothing.
func reportRerunCheck(opts options, st *runState, stdout, stderr io.Writer) int {
	rate, err := clock.ParseRate(opts.rate)
	if err != nil {
		fmt.Fprintf(stderr, "rerun check failed: %v\n", err)
		return exitInput
	}
	sb := sandbox{root: opts.out}
	again, _, err := analyze(sb, rate, opts.frames)
	if err != nil {
		fmt.Fprintf(stderr, "rerun check failed: %v\n", err)
		return exitInput
	}
	m, err := txn.ReadManifest(opts.out)
	if err != nil {
		fmt.Fprintf(stderr, "rerun check failed: %v\n", err)
		return exitInput
	}
	match := again.FrameDigest == st.summary.FrameDigest && m.Summary.FrameDigest == again.FrameDigest
	published := 0
	if entries, err := os.ReadDir(filepath.Join(opts.out, "artifacts", taskName)); err == nil {
		published = len(entries)
	}
	fmt.Fprintf(stdout, "rerun frame_sha256_match=%t frames=%d committed_artifacts=%d registered_artifacts=%d\n",
		match, again.Frames, published, len(m.Artifacts))
	if !match || published != len(m.Artifacts) {
		fmt.Fprintln(stderr, "rerun changed the published set")
		return exitInput
	}
	return exitOK
}

func doRecover(opts options, stdout, stderr io.Writer) int {
	eng, err := txn.OpenRoot(opts.out, txn.CrashNone)
	if err != nil {
		fmt.Fprintf(stderr, "recover failed: %v\n", err)
		return exitInput
	}
	defer eng.Close()
	res, err := eng.Recover()
	if err != nil {
		fmt.Fprintf(stderr, "recover failed: %v\n", err)
		return exitInput
	}
	fmt.Fprintf(stdout, "recovered=%d published=%d missing=%d duplicate=%d\n",
		res.Recovered, res.Published, res.Missing, res.Duplicate)
	if len(res.Cleaned) > 0 {
		fmt.Fprintf(stdout, "cleaned=%s\n", strings.Join(res.Cleaned, ","))
	}
	m, err := txn.ReadManifest(opts.out)
	if err != nil {
		if res.Published == 0 {
			return exitOK
		}
		fmt.Fprintf(stderr, "manifest unreadable after recovery: %v\n", err)
		return exitInput
	}
	if m.Summary != nil {
		printSummary(stdout, m.Summary, "recover")
	}
	for _, e := range res.Errors {
		fmt.Fprintf(stderr, "recovery note: %v\n", e)
	}
	return exitOK
}

func doVerify(opts options, stdout, stderr io.Writer) int {
	m, err := txn.ReadManifest(opts.out)
	if err != nil {
		fmt.Fprintf(stderr, "verify failed: %v\n", err)
		return exitInput
	}
	var missing, corrupt int
	for _, a := range m.Artifacts {
		path := filepath.Join(opts.out, filepath.FromSlash(a.File))
		data, err := os.ReadFile(path)
		if err != nil {
			missing++
			fmt.Fprintf(stderr, "missing artifact %s\n", a.File)
			continue
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			corrupt++
			fmt.Fprintf(stderr, "hash mismatch for %s\n", a.File)
		}
	}
	fmt.Fprintf(stdout, "verify artifacts=%d missing=%d corrupt=%d\n", len(m.Artifacts), missing, corrupt)
	if missing != 0 || corrupt != 0 {
		return exitInput
	}
	return exitOK
}

func doRollback(opts options, stdout, stderr io.Writer) int {
	eng, err := txn.OpenRoot(opts.out, txn.CrashNone)
	if err != nil {
		fmt.Fprintf(stderr, "rollback failed: %v\n", err)
		return exitInput
	}
	defer eng.Close()
	n, err := eng.Rollback(taskName)
	if err != nil {
		fmt.Fprintf(stderr, "rollback failed: %v\n", err)
		return exitInput
	}
	fmt.Fprintf(stdout, "rolled_back=%d\n", n)
	return exitOK
}
